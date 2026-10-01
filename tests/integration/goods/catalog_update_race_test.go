//go:build integration

package goods_test

import (
	"context"
	"errors"
	"fmt"
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

func TestCatalogUpdatesSerializeWithDelete(t *testing.T) {
	for _, table := range []string{"brands", "banner"} {
		for _, first := range []string{"update", "delete"} {
			t.Run(table+"/"+first+" first", func(t *testing.T) {
				db := testdb.Open(t, "grpctest_goods_srv")
				previous := global.DB
				global.DB = db
				t.Cleanup(func() { global.DB = previous })
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				server := &handler.GoodsServer{}
				brand := model.Brands{Name: fmt.Sprintf("edit-%d", time.Now().UnixNano()%1000000000000)}
				banner := model.Banner{Image: "old.png", Url: "old", Index: 3}
				var row interface{} = &brand
				if table == "banner" {
					row = &banner
				}
				if err := db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := db.Unscoped().Delete(row).Error; err != nil {
						t.Error(err)
					}
				})
				id := brand.ID
				if table == "banner" {
					id = banner.ID
				}
				update := func(ctx context.Context) error {
					if table == "brands" {
						_, err := server.UpdateBrand(ctx, &proto.BrandRequest{Id: id, Logo: "new.png"})
						return err
					}
					_, err := server.UpdateBanner(ctx, &proto.BannerRequest{Id: id, Image: "new.png", Url: "new", Index: 0})
					return err
				}
				remove := func(ctx context.Context) error {
					if table == "brands" {
						_, err := server.DeleteBrand(ctx, &proto.BrandRequest{Id: id})
						return err
					}
					_, err := server.DeleteBanner(ctx, &proto.BannerRequest{Id: id})
					return err
				}
				entered, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var enterOnce, releaseOnce, startOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				var calls sync.WaitGroup
				defer func() { unblock(); cancel(); calls.Wait() }()
				pause := func(tx *gorm.DB) {
					if tx.Error == nil && tx.Statement.Table == table && tx.Statement.Context.Value(referenceOrderKey{}) == "first" {
						enterOnce.Do(func() {
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
							}
						})
					}
				}
				start := func(tx *gorm.DB) {
					if tx.Statement.Table == table && tx.Statement.Context.Value(referenceOrderKey{}) == "second" {
						startOnce.Do(func() { close(started) })
					}
				}
				const pauseName, startName = "test:catalog-update-pause", "test:catalog-update-start"
				if first == "update" {
					if err := db.Callback().Query().After("gorm:query").Register(pauseName, pause); err != nil {
						t.Fatal(err)
					}
				} else if err := db.Callback().Delete().After("gorm:delete").Before("gorm:commit_or_rollback_transaction").Register(pauseName, pause); err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Query().Before("gorm:query").Register(startName, start); err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Delete().Before("gorm:delete").Register(startName, start); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					db.Callback().Query().Remove(pauseName)
					db.Callback().Delete().Remove(pauseName)
					db.Callback().Query().Remove(startName)
					db.Callback().Delete().Remove(startName)
				})
				firstCall, secondCall := update, remove
				wantSecond := codes.OK
				if first == "delete" {
					firstCall, secondCall = remove, update
					wantSecond = codes.NotFound
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
					t.Fatal("first call did not reach pause", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var probeRow struct{ ID int32 }
				probe := db.WithContext(ctx).Table(table).Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("id = ?", id).Take(&probeRow).Error
				secondCtx := context.WithValue(ctx, referenceOrderKey{}, "second")
				if probe == nil {
					// Reproduce the old unlocked lookup: delete fully before the update resumes.
					secondResult <- secondCall(secondCtx)
				} else {
					var locked *mysql.MySQLError
					if !errors.As(probe, &locked) || locked.Number != 3572 {
						t.Fatalf("unexpected lock probe: %v", probe)
					}
					calls.Add(1)
					go func() { defer calls.Done(); secondResult <- secondCall(secondCtx) }()
					select {
					case <-started:
					case err := <-secondResult:
						t.Fatal("second call did not reach SQL", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				unblock()
				firstErr, secondErr := <-firstResult, <-secondResult
				if firstErr != nil || status.Code(secondErr) != wantSecond {
					t.Fatalf("first=%v second=%v, want OK/%s", firstErr, secondErr, wantSecond)
				}
				if err := db.Unscoped().First(row, id).Error; err != nil {
					t.Fatal(err)
				}
				if table == "brands" {
					wantLogo := ""
					if first == "update" {
						wantLogo = "new.png"
					}
					if !brand.DeletedAt.Valid || brand.Logo != wantLogo {
						t.Fatalf("successful update must persist before deletion; deleted=%v logo=%q want=%q", brand.DeletedAt.Valid, brand.Logo, wantLogo)
					}
				} else {
					wantImage, wantURL, wantIndex := "old.png", "old", int32(3)
					if first == "update" {
						wantImage, wantURL, wantIndex = "new.png", "new", 0
					}
					if !banner.DeletedAt.Valid || banner.Image != wantImage || banner.Url != wantURL || banner.Index != wantIndex {
						t.Fatalf("successful update must persist before deletion: %+v", banner)
					}
				}
			})
		}
	}
}
