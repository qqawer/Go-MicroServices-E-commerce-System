package handler

import (
	"context"
	"errors"
	"testing"

	"grpctest/user_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAddressDatabaseErrorsAreNotSuccess(t *testing.T) {
	failure := errors.New("private database detail")
	for _, method := range []string{"list", "get", "create", "update", "delete"} {
		t.Run(method, func(t *testing.T) {
			mock := profileMockDB(t)
			server := &AddressServer{}
			ctx := context.Background()
			var err error
			switch method {
			case "list":
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT count.*FROM `user_address`").WithArgs(41).WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.ListAddresses(ctx, &proto.AddressListRequest{UserId: 41})
			case "get":
				mock.ExpectQuery("SELECT .*FROM `user_address`").WithArgs(7, 41, 1).WillReturnError(failure)
				_, err = server.GetAddress(ctx, &proto.AddressLookup{UserId: 41, Id: 7})
			case "create":
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT `id` FROM `user`.*FOR SHARE").WithArgs(41, 1).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
				mock.ExpectExec("INSERT INTO `user_address`").WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.CreateAddress(ctx, &proto.CreateAddressRequest{UserId: 41, Name: "小李", Mobile: "13800138000", Province: "浙江省", City: "杭州市", District: "西湖区", Address: "测试路 1 号"})
			case "update":
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT .*FROM `user_address`.*FOR UPDATE").WithArgs(7, 41, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "name", "mobile", "province", "city", "district", "address"}).AddRow(7, 41, "小李", "13800138000", "浙江省", "杭州市", "西湖区", "测试路 1 号"))
				mock.ExpectExec("UPDATE `user_address` SET `name`=\\?,`update_time`=\\? WHERE \\(id = \\? AND user_id = \\?\\) AND `user_address`.`deleted_at` IS NULL").WithArgs("小王", sqlmock.AnyArg(), 7, 41).WillReturnError(failure)
				mock.ExpectRollback()
				name := "小王"
				_, err = server.UpdateAddress(ctx, &proto.UpdateAddressRequest{UserId: 41, Id: 7, Name: &name})
			case "delete":
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE `user_address` SET `deleted_at`").WithArgs(sqlmock.AnyArg(), 7, 41).WillReturnError(failure)
				mock.ExpectRollback()
				_, err = server.DeleteAddress(ctx, &proto.AddressLookup{UserId: 41, Id: 7})
			}
			if status.Code(err) != codes.Internal || status.Convert(err).Message() != "收货地址操作失败" {
				t.Fatalf("database failure must return a safe Internal error: %v", err)
			}
		})
	}
}
