package handler

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"grpctest/user_srv/global"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MessagesServer struct {
	proto.UnimplementedMessagesServer
}

func messageDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, context.Canceled) {
		return status.FromContextError(context.Canceled).Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(context.DeadlineExceeded).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "用户或留言不存在")
	}
	return status.Error(codes.Internal, "留言操作失败")
}

func messageResponse(message model.UserMessage) *proto.UserMessage {
	return &proto.UserMessage{Id: message.ID, UserId: message.UserID, MessageType: message.MessageType,
		Subject: message.Subject, Message: message.Message, CreatedAt: message.CreatedAt.Unix()}
}

func validMessageLookup(req *proto.MessageLookup) error {
	if req == nil || req.UserId <= 0 || req.Id <= 0 {
		return status.Error(codes.InvalidArgument, "用户或留言 ID 无效")
	}
	return nil
}

func (s *MessagesServer) ListMessages(ctx context.Context, req *proto.MessageListRequest) (*proto.MessageListResponse, error) {
	if req == nil || req.UserId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	pn, size := req.Pn, req.Psize
	if pn == 0 {
		pn = 1
	}
	if size == 0 {
		size = 10
	}
	offset := (int64(pn) - 1) * int64(size)
	if pn < 1 || size < 1 || size > 100 || offset > math.MaxInt32 {
		return nil, status.Error(codes.InvalidArgument, "分页参数无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	var total int64
	var messages []model.UserMessage
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.UserMessage{}).Where("user_id = ?", req.UserId).Count(&total).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", req.UserId).Order("id DESC").Offset(int(offset)).Limit(int(size)).Find(&messages).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, messageDBError(ctx, err)
	}
	if total > math.MaxInt32 {
		return nil, status.Error(codes.ResourceExhausted, "留言数量超出支持范围")
	}
	response := &proto.MessageListResponse{Total: int32(total), Data: make([]*proto.UserMessage, 0, len(messages))}
	for _, message := range messages {
		response.Data = append(response.Data, messageResponse(message))
	}
	return response, nil
}

func (s *MessagesServer) GetMessage(ctx context.Context, req *proto.MessageLookup) (*proto.UserMessage, error) {
	if err := validMessageLookup(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	var message model.UserMessage
	if err := global.DB.WithContext(ctx).Where("id = ? AND user_id = ?", req.Id, req.UserId).First(&message).Error; err != nil {
		return nil, messageDBError(ctx, err)
	}
	return messageResponse(message), nil
}

func (s *MessagesServer) CreateMessage(ctx context.Context, req *proto.CreateMessageRequest) (*proto.UserMessage, error) {
	if req == nil || req.UserId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	if req.MessageType < 1 || req.MessageType > 5 {
		return nil, status.Error(codes.InvalidArgument, "留言类型须为 1–5")
	}
	message := model.UserMessage{UserID: req.UserId, MessageType: req.MessageType,
		Subject: strings.TrimSpace(req.Subject), Message: strings.TrimSpace(req.Message)}
	for _, field := range []struct {
		value string
		max   int
	}{{message.Subject, 100}, {message.Message, 2000}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) < 1 || utf8.RuneCountInString(field.value) > field.max {
			return nil, status.Error(codes.InvalidArgument, "留言主题须为 1–100 个字符，内容须为 1–2000 个字符")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&user, req.UserId).Error; err != nil {
			return err
		}
		return tx.Create(&message).Error
	})
	if err != nil {
		return nil, messageDBError(ctx, err)
	}
	return messageResponse(message), nil
}

func (s *MessagesServer) DeleteMessage(ctx context.Context, req *proto.MessageLookup) (*emptypb.Empty, error) {
	if err := validMessageLookup(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	result := global.DB.WithContext(ctx).Where("id = ? AND user_id = ?", req.Id, req.UserId).Delete(&model.UserMessage{})
	if result.Error != nil {
		return nil, messageDBError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "留言不存在")
	}
	return &emptypb.Empty{}, nil
}
