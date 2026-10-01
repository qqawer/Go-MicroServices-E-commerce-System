//go:build integration

package goods_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"
)

func TestCategoryLevelChangeRacesWithChildWrites(t *testing.T) {
	for _, operation := range []string{"create", "move"} {
		for _, first := range []string{"child", "parent"} {
			t.Run(operation+"/"+first+" first", func(t *testing.T) {
				db := testdb.Open(t, "grpctest_goods_srv")
				previous := global.DB
				global.DB = db
				t.Cleanup(func() { global.DB = previous })
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				server := &handler.GoodsServer{}
				var ids []int32
				t.Cleanup(func() {
					// All fixtures and newly created children belong to these roots.
					db.Unscoped().Where("parent_category_id IN ?", ids).Delete(&model.Category{})
					db.Unscoped().Where("id IN ?", ids).Delete(&model.Category{})
				})
				create := func(level, parent int32) int32 {
					r, err := server.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Race hierarchy", Level: level, ParentCategory: parent})
					if err != nil {
						t.Fatal(err)
					}
					ids = append(ids, r.Id)
					return r.Id
				}
				target, other := create(1, 0), create(1, 0)
				child := create(2, other)
				writeChild := func(ctx context.Context) error {
					if operation == "create" {
						_, err := server.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Concurrent child", Level: 2, ParentCategory: target})
						return err
					}
					_, err := server.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: child, ParentCategory: target})
					return err
				}
				changeLevel := func(ctx context.Context) error {
					_, err := server.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: target, ParentCategory: other, Level: 2})
					return err
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var enterOnce, releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				var calls sync.WaitGroup
				defer func() { unblock(); cancel(); calls.Wait() }()
				callback := "test:hierarchy-lock"
				if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
					row, ok := tx.Statement.Dest.(*model.Category)
					if ok && tx.Error == nil && row.ID == target && tx.Statement.Context.Value(referenceOrderKey{}) == "first" {
						enterOnce.Do(func() {
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
							}
						})
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Callback().Query().Remove(callback) })
				firstCall, secondCall := writeChild, changeLevel
				want := codes.FailedPrecondition
				if first == "parent" {
					firstCall, secondCall = changeLevel, writeChild
					want = codes.InvalidArgument
				}
				firstResult, secondResult := make(chan error, 1), make(chan error, 1)
				calls.Add(1)
				go func() {
					defer calls.Done()
					firstResult <- firstCall(context.WithValue(ctx, referenceOrderKey{}, "first"))
				}()
				select {
				case <-entered:
				case err := <-firstResult:
					t.Fatal("first call did not acquire target lock", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var locked *mysql.MySQLError
				probe := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&model.Category{}, target).Error
				if !errors.As(probe, &locked) || locked.Number != 3572 {
					t.Fatalf("target row must be locked: %v", probe)
				}
				calls.Add(1)
				go func() { defer calls.Done(); secondResult <- secondCall(ctx) }()
				unblock()
				firstErr, secondErr := <-firstResult, <-secondResult
				if firstErr != nil || status.Code(secondErr) != want {
					t.Fatalf("first=%v second=%v; want OK/%s", firstErr, secondErr, want)
				}
				var invalid int64
				if err := db.Raw("SELECT COUNT(*) FROM category c JOIN category p ON p.id=c.parent_category_id WHERE c.deleted_at IS NULL AND (p.deleted_at IS NOT NULL OR c.level<>p.level+1)").Scan(&invalid).Error; err != nil || invalid != 0 {
					t.Fatalf("invalid committed hierarchy: count=%d error=%v", invalid, err)
				}
			})
		}
	}
}
