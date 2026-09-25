package handler

import (
	"context"
	"errors"
	"strings"
	"time"
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

// PatchUser is a separate method so a new Web cannot accidentally send a partial
// update to an old server whose Updateuser still overwrites omitted fields.
func (s *UserServer) PatchUser(ctx context.Context, req *proto.PatchUserInfo) (*emptypb.Empty, error) {
	if req == nil || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	updates, err := profileTextUpdates(req.NickName, req.Gender)
	if err != nil {
		return nil, err
	}
	if req.BirthdayDate != nil {
		if *req.BirthdayDate == "" {
			updates["birthday"] = nil
		} else {
			birthday, err := parseProfileBirthday(*req.BirthdayDate)
			if err != nil {
				return nil, err
			}
			// DATETIME stores calendar fields. A string avoids the MySQL driver
			// converting a time.Time through its DSN location and shifting the day.
			updates["birthday"] = birthday.Format("2006-01-02")
		}
	}
	return updateProfile(ctx, req.Id, updates)
}

// Updateuser retains the legacy timestamp contract, but no longer writes fields
// omitted by the caller. New Web profile requests use PatchUser instead.
func (s *UserServer) Updateuser(ctx context.Context, req *proto.UpdateUserInfo) (*emptypb.Empty, error) {
	if req == nil || req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 无效")
	}
	updates, err := profileTextUpdates(req.NickName, req.Gender)
	if err != nil {
		return nil, err
	}
	if req.BirthDay != nil {
		birthday := time.Unix(int64(*req.BirthDay), 0)
		if _, err := parseProfileBirthday(birthday.Format("2006-01-02")); err != nil {
			return nil, err
		}
		updates["birthday"] = birthday
	}
	return updateProfile(ctx, req.Id, updates)
}

func profileTextUpdates(nickname, gender *string) (map[string]interface{}, error) {
	updates := make(map[string]interface{})
	if nickname != nil {
		name := strings.TrimSpace(*nickname)
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 20 {
			return nil, status.Error(codes.InvalidArgument, "昵称须为 1–20 个字符")
		}
		updates["nick_name"] = name
	}
	if gender != nil {
		if *gender != "male" && *gender != "female" {
			return nil, status.Error(codes.InvalidArgument, "性别须为 male 或 female")
		}
		updates["gender"] = *gender
	}
	return updates, nil
}

func parseProfileBirthday(value string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil || date.Year() < 1000 || date.Format("2006-01-02") != value || value > time.Now().UTC().Format("2006-01-02") {
		return time.Time{}, status.Error(codes.InvalidArgument, "生日须为有效的 YYYY-MM-DD，且不能晚于今天")
	}
	return date, nil
}

func updateProfile(ctx context.Context, id int32, updates map[string]interface{}) (*emptypb.Empty, error) {
	if len(updates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "至少提交一个资料字段")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock only this user's row and check existence before updating. An unchanged
		// value can produce zero affected rows in MySQL; it is still a valid retry.
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&user, id).Error; err != nil {
			return err
		}
		// Explicit columns prevent unrelated profile, password, mobile or role
		// values from being written back from a stale whole-row snapshot.
		return tx.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
	})
	if err != nil {
		return nil, profileDBError(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

func profileDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "用户不存在")
	}
	return status.Error(codes.Internal, "用户资料操作失败")
}
