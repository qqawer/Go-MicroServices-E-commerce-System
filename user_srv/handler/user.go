package handler

import (
	"context"
	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"
	"grpctest/user_srv/utils"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type UserServer struct {
	proto.UnimplementedUserServer // 关键嵌入
}

func ModelToResponse(user model.User) *proto.UserInfoResponse {
	//在grpc的message中字段有默认值，你就不能随便赋值nil进去，容易出错
	//这里要搞清，哪些字段是有默认值的
	UserInfoRsp := &proto.UserInfoResponse{
		Id:       user.ID,
		Password: user.Password,
		NickName: user.NickName,
		Gender:   user.Gender,
		Role:     int32(user.Role),
	}
	if user.Birthday != nil {
		UserInfoRsp.Birthday = uint64(user.Birthday.Unix())
	}
	return UserInfoRsp
}

func (s *UserServer) GetUserList(ctx context.Context, req *proto.PageInfo) (*proto.UserListResponse, error) {
	//获取用户列表
	var users []model.User
	result := global.DB.Find(&users)
	if result.Error != nil {
		return nil, result.Error
	}
	rsp := &proto.UserListResponse{}
	rsp.Total = int32(result.RowsAffected)

	global.DB.Scopes(utils.Paginate(int(req.Pn), int(req.PSize))).Find(&users)

	for _, user := range users {
		userInfoRsp := ModelToResponse(user)
		rsp.Data = append(rsp.Data, userInfoRsp)
	}
	return rsp, nil
}
func (s *UserServer) GetUserByMobile(ctx context.Context, req *proto.MobileRequest) (*proto.UserInfoResponse, error) {
	//通过手机号查询用户
	var user model.User
	result := global.DB.Where("mobile = ?", req.Mobile).First(&user)
	if result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "用户不存在")
	}
	if result.Error != nil {
		return nil, result.Error
	}
	userInfoRsp := ModelToResponse(user)
	return userInfoRsp, nil
}
func (s *UserServer) GetUserById(ctx context.Context, req *proto.IdRequest) (*proto.UserInfoResponse, error) {
	//通过id查询用户
	var user model.User
	result := global.DB.First(&user, req.Id)
	if result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "用户不存在")
	}
	if result.Error != nil {
		return nil, result.Error
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
func (s *UserServer) Updateuser(ctx context.Context, req *proto.UpdateUserInfo) (*emptypb.Empty, error) {
	//个人中心更新用户
	var user model.User
	result := global.DB.First(&user, req.Id)
	if result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "用户不存在")
	}

	birthday := time.Unix(int64(req.BirthDay), 0)
	user.NickName = req.NickName
	user.Birthday = &birthday
	user.Gender = req.Gender

	result = global.DB.Save(&user)
	if result.Error != nil {
		return nil, status.Errorf(codes.Internal, result.Error.Error())
	}
	return &emptypb.Empty{}, nil

}
func (s *UserServer) CheckPassWord(ctx context.Context, req *proto.PasswordCheckInfo) (*proto.CheckResponse, error) {
	//校验密码
	check := utils.CheckPasswordHash(req.Password, req.EncryptedPassword)
	return &proto.CheckResponse{Success: check}, nil
}
