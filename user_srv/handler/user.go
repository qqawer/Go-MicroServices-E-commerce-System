package handler

import (
	"context"
	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"
	"grpctest/user_srv/utils"
	"math"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type UserServer struct {
	proto.UnimplementedUserServer // 关键嵌入
}

func ModelToResponse(user model.User) *proto.UserInfoResponse {
	UserInfoRsp := &proto.UserInfoResponse{
		Id:       user.ID,
		Password: user.Password,
		Mobile:   user.Mobile,
		NickName: user.NickName,
		Gender:   user.Gender,
		Role:     int32(user.Role),
	}
	if user.Birthday != nil {
		// 保留字段存在性，区分数据库 NULL 和真实的 Unix 时间戳 0。
		birthday := uint64(user.Birthday.Unix())
		UserInfoRsp.Birthday = &birthday
		birthdayDate := user.Birthday.Format("2006-01-02")
		UserInfoRsp.BirthdayDate = &birthdayDate
	}
	return UserInfoRsp
}

func (s *UserServer) GetUserList(ctx context.Context, req *proto.PageInfo) (*proto.UserListResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "用户分页参数无效")
	}
	page, size := int64(req.Pn), int64(req.PSize)
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 10
	} else if size > 100 {
		size = 100
	}
	offset := (page - 1) * size
	if offset > math.MaxInt32 {
		return nil, status.Error(codes.InvalidArgument, "用户分页参数无效")
	}
	query := global.DB.WithContext(ctx).Model(&model.User{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, userListReadError(ctx)
	}
	if total > math.MaxInt32 {
		return nil, status.Error(codes.OutOfRange, "用户总数超出支持范围")
	}
	var users []model.User
	if err := query.Order("id ASC").Offset(int(offset)).Limit(int(size)).Find(&users).Error; err != nil {
		return nil, userListReadError(ctx)
	}
	rsp := &proto.UserListResponse{Total: int32(total)}

	for _, user := range users {
		userInfoRsp := ModelToResponse(user)
		rsp.Data = append(rsp.Data, userInfoRsp)
	}
	return rsp, nil
}
func userListReadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Unavailable, "用户列表暂时无法读取")
}

func (s *UserServer) GetUserByMobile(ctx context.Context, req *proto.MobileRequest) (*proto.UserInfoResponse, error) {
	//通过手机号查询用户
	var user model.User
	if err := global.DB.WithContext(ctx).Where("mobile = ?", req.Mobile).First(&user).Error; err != nil {
		return nil, profileDBError(ctx, err)
	}
	userInfoRsp := ModelToResponse(user)
	return userInfoRsp, nil
}
func (s *UserServer) GetUserById(ctx context.Context, req *proto.IdRequest) (*proto.UserInfoResponse, error) {
	if req == nil || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	var user model.User
	if err := global.DB.WithContext(ctx).First(&user, req.Id).Error; err != nil {
		return nil, profileDBError(ctx, err)
	}
	userInfoRsp := ModelToResponse(user)
	return userInfoRsp, nil
}
func (s *UserServer) CreateUser(ctx context.Context, req *proto.CreateUserInfo) (*proto.UserInfoResponse, error) {
	//创建用户
	var user model.User
	result := global.DB.Where(&model.User{Mobile: req.Mobile}).First(&user)
	if result.RowsAffected == 1 {
		return nil, status.Errorf(codes.AlreadyExists, "用户已存在")
	}
	user.Mobile = req.Mobile
	user.NickName = req.NickName

	//密码加密
	newPassword, err := utils.HashPassword(req.PassWord)
	if err != nil {
		return nil, err
	}
	user.Password = newPassword

	result = global.DB.Create(&user)
	if result.Error != nil {
		return nil, status.Errorf(codes.Internal, result.Error.Error())
	}

	userInfoRsp := ModelToResponse(user)
	return userInfoRsp, nil
}
func (s *UserServer) CheckPassWord(ctx context.Context, req *proto.PasswordCheckInfo) (*proto.CheckResponse, error) {
	//校验密码
	check := utils.CheckPasswordHash(req.Password, req.EncryptedPassword)
	return &proto.CheckResponse{Success: check}, nil
}
