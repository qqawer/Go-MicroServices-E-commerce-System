//go:build integration

package user_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"grpctest/tests/integration/testdb"
	"grpctest/user_srv/global"
	"grpctest/user_srv/handler"
	"grpctest/user_srv/model/proto"
	model "grpctest/user_srv/model/user"
)

func TestConcurrentRegistrationAndDeletedMobile(t *testing.T) {
	db := testdb.Open(t, "grpctest_user_srv")
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	const mobile = "13001110001"
	t.Cleanup(func() {
		if err := db.Unscoped().Where("mobile = ?", mobile).Delete(&model.User{}).Error; err != nil {
			t.Error(err)
		}
	})
	_, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterUserServer(server, &handler.UserServer{}) })
	client := proto.NewUserClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Both real SELECTs must miss before either RPC can hash and INSERT.
	// This exercises the unique-key race, not just the preflight exists branch.
	ready := make(chan struct{})
	var reads atomic.Int32
	callback := "test:registration-race"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "user" && errors.Is(tx.Error, gorm.ErrRecordNotFound) && len(tx.Statement.Vars) > 0 && tx.Statement.Vars[0] == mobile {
			if reads.Add(1) == 2 {
				close(ready)
			}
			select {
			case <-ready:
			case <-ctx.Done():
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(callback) })
	type result struct {
		user *proto.UserInfoResponse
		err  error
	}
	results := make(chan result, 2)
	var calls sync.WaitGroup
	defer func() { cancel(); calls.Wait() }()
	for _, name := range []string{"First applicant", "Second applicant"} {
		calls.Add(1)
		go func(name string) {
			defer calls.Done()
			user, err := client.CreateUser(ctx, &proto.CreateUserInfo{Mobile: mobile, NickName: name, PassWord: "test-only-password"})
			results <- result{user, err}
		}(name)
	}
	var winner *proto.UserInfoResponse
	success, duplicate := 0, 0
	for i := 0; i < 2; i++ {
		r := <-results
		switch status.Code(r.err) {
		case codes.OK:
			success++
			winner = r.user
		case codes.AlreadyExists:
			duplicate++
		default:
			t.Fatalf("unexpected registration result: %v", r.err)
		}
	}
	if success != 1 || duplicate != 1 || reads.Load() != 2 {
		t.Fatalf("success=%d duplicate=%d initial misses=%d", success, duplicate, reads.Load())
	}
	var rows []model.User
	if err := db.Unscoped().Where("mobile = ?", mobile).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("expected one persisted user: count=%d error=%v", len(rows), err)
	}
	stored := rows[0]
	if stored.ID != winner.GetId() || stored.NickName != winner.GetNickName() || stored.Role != 1 || stored.Password == "" || stored.Password == "test-only-password" {
		t.Fatal("winning user's fields were not preserved")
	}
	if _, err := client.CreateUser(ctx, &proto.CreateUserInfo{Mobile: mobile, NickName: "Must not replace", PassWord: "other-password"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("existing mobile: %v", err)
	}
	if err := db.Delete(&model.User{}, stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateUser(ctx, &proto.CreateUserInfo{Mobile: mobile, NickName: "Must not restore", PassWord: "other-password"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("deleted mobile unique key: %v", err)
	}
	rows = nil
	if err := db.Unscoped().Where("mobile = ?", mobile).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("duplicate added a row: %v", err)
	}
	if rows[0].ID != stored.ID || rows[0].Password != stored.Password || rows[0].NickName != stored.NickName || rows[0].Role != stored.Role || !rows[0].DeletedAt.Valid {
		t.Fatal("duplicate request changed or restored the existing account")
	}
}
