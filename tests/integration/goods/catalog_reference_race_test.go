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

type referenceOrderKey struct{}

// Pause real SQL after the first operation locks the target. NOWAIT on a separate
// connection proves the row lock is held; channels order the competing calls.
func TestCatalogDeleteRacesWithReferenceWriters(t *testing.T) {
	for _, operation := range []string{"goods create", "goods update", "relation create", "relation update", "child create", "child move"} {
		targets := []string{"category", "brands"}
		if operation == "child create" || operation == "child move" {
			targets = targets[:1]
		}
		for _, target := range targets {
			for _, first := range []string{"write", "delete"} {
				t.Run(operation+"/"+target+"/"+first+" first", func(t *testing.T) {
					db := testdb.Open(t, "grpctest_goods_srv")
					previous := global.DB
					global.DB = db
					t.Cleanup(func() { global.DB = previous })
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					server := &handler.GoodsServer{}
					cat := model.Category{Name: "Race target", Level: 1}
					oldCat := model.Category{Name: "Race source", Level: 1}
					brand := model.Brands{Name: fmt.Sprintf("race-%d", time.Now().UnixNano()%1000000000000)}
					for _, row := range []*model.Category{&cat, &oldCat} {
						if err := db.Omit("ParentCategoryID", clause.Associations).Create(row).Error; err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Create(&brand).Error; err != nil {
						t.Fatal(err)
					}
					child := model.Category{Name: "Race child", Level: 2, ParentCategoryID: oldCat.ID}
					if err := db.Omit(clause.Associations).Create(&child).Error; err != nil {
						t.Fatal(err)
					}
					relation := model.GoodsCategoryBrand{CategoryID: oldCat.ID, BrandsID: 1}
					if err := db.Omit(clause.Associations).Create(&relation).Error; err != nil {
						t.Fatal(err)
					}
					var goods model.Goods
					if err := db.First(&goods, 421).Error; err != nil {
						t.Fatal(err)
					}
					goods.ID = 0
					goods.InventoryReady = true
					goods.GoodsSn = "RACE-REFERENCE"
					if err := db.Omit(clause.Associations).Create(&goods).Error; err != nil {
						t.Fatal(err)
					}
					requestKey := fmt.Sprintf("reference-race-%d", time.Now().UnixNano())
					t.Cleanup(func() {
						var ids []int32
						db.Model(&model.GoodsCreation{}).Where("request_key = ?", requestKey).Pluck("goods_id", &ids)
						db.Unscoped().Where("request_key = ?", requestKey).Delete(&model.GoodsCreation{})
						ids = append(ids, goods.ID)
						db.Unscoped().Where("id IN ?", ids).Delete(&model.Goods{})
						db.Unscoped().Where("category_id IN ?", []int32{cat.ID, oldCat.ID}).Delete(&model.GoodsCategoryBrand{})
						db.Unscoped().Where("parent_category_id IN ?", []int32{cat.ID, oldCat.ID}).Delete(&model.Category{})
						db.Unscoped().Where("id IN ?", []int32{cat.ID, oldCat.ID}).Delete(&model.Category{})
						db.Unscoped().Delete(&model.Brands{}, brand.ID)
					})
					targetID := cat.ID
					if target == "brands" {
						targetID = brand.ID
					}
					write := func(ctx context.Context) error {
						switch operation {
						case "goods create", "goods update":
							req := &proto.CreateGoodsInfo{RequestKey: requestKey, Name: "Race goods", GoodsSn: "RACE-WRITE", CategoryId: cat.ID, BrandId: brand.ID}
							if operation == "goods update" {
								req.Id = goods.ID
								_, err := server.UpdateGoods(ctx, req)
								return err
							}
							_, err := server.CreateGoods(ctx, req)
							return err
						case "relation create":
							_, err := server.CreateCategoryBrand(ctx, &proto.CategoryBrandRequest{CategoryId: cat.ID, BrandId: brand.ID})
							return err
						case "relation update":
							_, err := server.UpdateCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: relation.ID, CategoryId: cat.ID, BrandId: brand.ID})
							return err
						case "child create":
							_, err := server.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Race new child", Level: 2, ParentCategory: cat.ID})
							return err
						default:
							_, err := server.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: child.ID, ParentCategory: cat.ID})
							return err
						}
					}
					remove := func(ctx context.Context) error {
						if target == "brands" {
							_, err := server.DeleteBrand(ctx, &proto.BrandRequest{Id: targetID})
							return err
						}
						_, err := server.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: targetID})
						return err
					}
					entered, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var enterOnce, startOnce, releaseOnce sync.Once
					unblock := func() { releaseOnce.Do(func() { close(release) }) }
					var calls sync.WaitGroup
					defer func() { unblock(); cancel(); calls.Wait() }()
					afterName, beforeName := "audit:reference-after", "audit:reference-before"
					if err := db.Callback().Query().After("gorm:query").Register(afterName, func(tx *gorm.DB) {
						matchesID := false
						switch value := tx.Statement.Dest.(type) {
						case *model.Category:
							matchesID = value.ID == targetID
						case *model.Brands:
							matchesID = value.ID == targetID
						}
						if tx.Statement.Table == target && matchesID && tx.Statement.Context.Value(referenceOrderKey{}) == "first" && tx.Error == nil {
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
					if err := db.Callback().Query().Before("gorm:query").Register(beforeName, func(tx *gorm.DB) {
						if tx.Statement.Table == target && tx.Statement.Context.Value(referenceOrderKey{}) == "second" {
							startOnce.Do(func() { close(started) })
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { db.Callback().Query().Remove(afterName); db.Callback().Query().Remove(beforeName) })
					firstCall, secondCall := write, remove
					if first == "delete" {
						firstCall, secondCall = remove, write
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
						t.Fatal("first call did not reach lock", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					var row struct{ ID int32 }
					probe := db.WithContext(ctx).Table(target).Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("id = ?", targetID).Take(&row).Error
					var locked *mysql.MySQLError
					if !errors.As(probe, &locked) || locked.Number != 3572 {
						unblock()
						<-firstResult
						t.Fatalf("target reference row is not locked: %v", probe)
					}
					calls.Add(1)
					go func() {
						defer calls.Done()
						secondResult <- secondCall(context.WithValue(ctx, referenceOrderKey{}, "second"))
					}()
					select {
					case <-started:
					case err := <-secondResult:
						unblock()
						<-firstResult
						t.Fatal("second call did not query target", err)
					case <-ctx.Done():
						unblock()
						<-firstResult
						<-secondResult
						t.Fatal(ctx.Err())
					}
					unblock()
					firstErr, secondErr := <-firstResult, <-secondResult
					want := codes.FailedPrecondition
					if first == "delete" {
						want = codes.InvalidArgument
					}
					if firstErr != nil || status.Code(secondErr) != want {
						t.Fatalf("first=%v second=%v, want OK/%s", firstErr, secondErr, want)
					}
					var missing int64
					// Assert committed live rows never point at a deleted target after the race.
					for _, query := range []string{
						"SELECT COUNT(*) FROM goods g JOIN category c ON c.id=g.category_id JOIN brands b ON b.id=g.brands_id WHERE g.deleted_at IS NULL AND (c.deleted_at IS NOT NULL OR b.deleted_at IS NOT NULL)",
						"SELECT COUNT(*) FROM goods_category_brand r JOIN category c ON c.id=r.category_id JOIN brands b ON b.id=r.brands_id WHERE r.deleted_at IS NULL AND (c.deleted_at IS NOT NULL OR b.deleted_at IS NOT NULL)",
						"SELECT COUNT(*) FROM category c JOIN category p ON p.id=c.parent_category_id WHERE c.deleted_at IS NULL AND p.deleted_at IS NOT NULL",
					} {
						if err := db.Raw(query).Scan(&missing).Error; err != nil || missing != 0 {
							t.Fatalf("dangling reference count=%d error=%v", missing, err)
						}
					}
				})
			}
		}
	}
}
