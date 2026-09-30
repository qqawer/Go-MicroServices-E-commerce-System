//go:build integration

package user_test

import (
	"fmt"
	"reflect"
	"testing"

	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"

	"google.golang.org/grpc"
)

func TestUserListPaginationAndSoftDelete(t *testing.T) {
	db := testdb.Open(t, "grpctest_user_srv")
	previous := global.DB
	global.DB = db
	var fixtureIDs []int32
	t.Cleanup(func() {
		if len(fixtureIDs) > 0 {
			db.Unscoped().Delete(&model.User{}, fixtureIDs)
		}
		global.DB = previous
	})
	for n := 0; n < 3; n++ {
		user := model.User{Mobile: fmt.Sprintf("1420000000%d", n), NickName: "list fixture", Password: "list-fixture-only", Role: 1}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal("create isolated list fixture", err)
		}
		fixtureIDs = append(fixtureIDs, user.ID)
		if n == 1 {
			if err := db.Delete(&user).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var expected []int32
	if err := db.Model(&model.User{}).Order("id ASC").Pluck("id", &expected).Error; err != nil {
		t.Fatal(err)
	}
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterUserServer(server, &handler.UserServer{}) })
	client := proto.NewUserClient(conn)
	var seen []int32
	for page := uint32(1); page <= uint32(len(expected)/2+2); page++ {
		rsp, err := client.GetUserList(ctx, &proto.PageInfo{Pn: page, PSize: 2})
		if err != nil || rsp.GetTotal() != int32(len(expected)) || len(rsp.GetData()) > 2 {
			t.Fatalf("invalid user page: %v %v", rsp, err)
		}
		for _, row := range rsp.Data {
			seen = append(seen, row.Id)
		}
	}
	if !reflect.DeepEqual(seen, expected) {
		t.Fatalf("unstable or incomplete pages: got %v want %v", seen, expected)
	}
	for _, id := range seen {
		if id == fixtureIDs[1] {
			t.Fatal("soft-deleted user returned")
		}
	}
	rsp, err := client.GetUserList(ctx, &proto.PageInfo{})
	if err != nil || rsp.GetTotal() != int32(len(expected)) || len(rsp.GetData()) != min(10, len(expected)) {
		t.Fatal("legacy defaults changed", err)
	}
}
