//go:build integration

package user_test

import (
	"context"
	"testing"
	"time"

	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

func TestUserCreateReadAndPassword(t *testing.T) {
	previous := global.DB
	global.DB = testdb.Open(t, "grpctest_user_srv")
	t.Cleanup(func() { global.DB = previous })
	_, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterUserServer(server, &handler.UserServer{}) })
	client := proto.NewUserClient(conn)
	requireRPC := func(operation string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s RPC failed (gRPC code=%s)", operation, status.Code(err))
		}
	}
	// Production bcrypt uses cost 14. Under -race each hash/compare is expensive;
	// give each operation its own deadline instead of sharing one for all calls.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	fixture, err := client.GetUserById(ctx, &proto.IdRequest{Id: 1})
	cancel()
	requireRPC("read seeded user", err)
	if fixture.GetNickName() != "integration_user" || fixture.GetRole() != 1 {
		t.Fatal("seeded user cannot be read through gRPC")
	}
	const password = "integration-test-only"
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	created, err := client.CreateUser(ctx, &proto.CreateUserInfo{Mobile: "13000000002", NickName: "created_in_test", PassWord: password})
	cancel()
	requireRPC("create user", err)
	if created.GetId() <= 1 {
		t.Fatal("user creation failed")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	user, err := client.GetUserById(ctx, &proto.IdRequest{Id: created.Id})
	cancel()
	requireRPC("read created user", err)
	if user.GetNickName() != "created_in_test" || user.GetRole() != 1 || user.GetPassword() == "" || user.GetPassword() == password {
		t.Fatal("created user was not persisted with a hashed password")
	}
	for _, tc := range []struct {
		password string
		valid    bool
	}{{password, true}, {"wrong-password", false}} {
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		checked, err := client.CheckPassWord(ctx, &proto.PasswordCheckInfo{Password: tc.password, EncryptedPassword: user.Password})
		cancel()
		requireRPC("verify password", err)
		if checked.GetSuccess() != tc.valid {
			t.Fatalf("password verification returned success=%t, want %t", checked.GetSuccess(), tc.valid)
		}
	}
}
