package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"grpctest/goods_srv/config"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/initialize"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	invproto "grpctest/inventory_srv/model/proto"

	"github.com/elastic/go-elasticsearch/v8"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

type hooksInventory struct{ invproto.InventoryClient }

func (hooksInventory) InitInv(_ context.Context, req *invproto.GoodsInventoryInfo, _ ...grpc.CallOption) (*invproto.GoodsInventoryInfo, error) {
	return req, nil
}

// This exercises the real RPC handlers, GORM/MySQL transactions and ES 8. It
// creates and removes its own database/index; the 40/420-product labs are untouched.
func TestGoodsESHooksIntegration(t *testing.T) {
	dsn, endpoint := os.Getenv("GOODS_ES_TEST_MYSQL_DSN"), os.Getenv("GOODS_ES_TEST_URL")
	if dsn == "" || endpoint == "" {
		t.Skip("set GOODS_ES_TEST_MYSQL_DSN and GOODS_ES_TEST_URL for the isolated local integration test")
	}
	settings, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test MySQL DSN")
	}
	if settings.Addr != "mysql:3306" && settings.Addr != "127.0.0.1:3306" {
		t.Fatal("this integration test requires the local lab MySQL endpoint")
	}
	settings.DBName = ""
	admin, err := sql.Open("mysql", settings.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	database, index := "es_goods_hooks_test_"+suffix, "grpctest-goods-hooks-test-"+suffix
	if _, err := admin.Exec("CREATE DATABASE " + database + " CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec("DROP DATABASE " + database); err != nil {
			t.Error(err)
		}
	}()
	settings.DBName, settings.ParseTime = database, true
	db, err := gorm.Open(mysql.Open(settings.FormatDSN()), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true}, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := db.AutoMigrate(&model.Category{}, &model.Brands{}, &model.Goods{}, &model.GoodsCreation{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Omit("ParentCategoryID").Create(&model.Category{BaseModel: model.BaseModel{ID: 1}, Name: "钩子实验分类", URL: "hooks-test", Level: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Brands{BaseModel: model.BaseModel{ID: 1}, Name: "钩子实验品牌"}).Error; err != nil {
		t.Fatal(err)
	}
	previousDB, previousES, previousIndex := global.DB, global.EsClient, global.EsIndex
	defer func() { global.DB, global.EsClient, global.EsIndex = previousDB, previousES, previousIndex }()
	global.DB = db
	if err := initialize.InitES(config.ElasticsearchConfig{Enabled: true, URL: endpoint, Index: index}); err != nil {
		t.Fatal(err)
	}
	client := global.EsClient
	defer client.Close(context.Background())
	esRequest := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Perform(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, data
	}
	defer func() {
		code, body := esRequest(http.MethodDelete, "/"+index, nil)
		if code != http.StatusOK {
			t.Errorf("clean test index: %d %s", code, body)
		}
	}()
	// A service restart must accept the mapping returned by ES, not only create it.
	if err := initialize.InitES(config.ElasticsearchConfig{Enabled: true, URL: endpoint, Index: index}); err != nil {
		t.Fatalf("reopen existing index: %v", err)
	}
	global.EsClient.Close(context.Background())
	global.EsClient = client
	t.Log("PASS startup: create index and reopen its existing mapping")
	readES := func(id int32) model.EsGoods {
		t.Helper()
		code, body := esRequest(http.MethodGet, fmt.Sprintf("/%s/_doc/%d", index, id), nil)
		var result struct {
			Source model.EsGoods `json:"_source"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &result) != nil {
			t.Fatalf("read ES document: %d %s", code, body)
		}
		return result.Source
	}
	server := &handler.GoodsServer{}
	request := &proto.CreateGoodsInfo{RequestKey: "hooks-create-001", CategoryId: 1, BrandId: 1,
		Name: "同步测试苹果", GoodsSn: "hooks-001", GoodsBrief: "用于钩子同步验证", ShopPrice: 44.9,
		MarketPrice: 50, Stocks: 5, OnSale: true, IsNew: true, ShipFree: true,
		Images: []string{}, DescImages: []string{}}
	created, err := server.CreateGoods(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Id
	doc := readES(id)
	if doc.ID != id || doc.Name != request.Name || doc.PriceCents != 4490 || doc.InventoryReady == nil || *doc.InventoryReady || doc.OnSale {
		t.Fatalf("AfterCreate must index an unready product with the correct price: %+v", doc)
	}
	t.Log("PASS AfterCreate: ES contains the complete pending product, 44.9 yuan -> 4490 cents")

	server.Inventory = hooksInventory{}
	confirmed, err := server.CreateGoods(context.Background(), request)
	if err != nil || !confirmed.InventoryReady {
		t.Fatalf("inventory confirmation: %v %v", confirmed, err)
	}
	doc = readES(id)
	if doc.InventoryReady == nil || !*doc.InventoryReady || !doc.OnSale || doc.Name != request.Name {
		t.Fatalf("inventory confirmation must update ES: %+v", doc)
	}
	t.Log("PASS inventory-ready update: same document becomes ready/on-sale")

	update := gproto.Clone(request).(*proto.CreateGoodsInfo)
	update.Id, update.Name, update.ShopPrice, update.ShipFree = id, "改名后的苹果", 39.9, false
	if _, err := server.UpdateGoods(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	doc = readES(id)
	if doc.Name != update.Name || doc.PriceCents != 3990 || doc.ShipFree || !doc.IsNew || !doc.OnSale || doc.Brief != request.GoodsBrief {
		t.Fatalf("partial model update lost product fields: %+v", doc)
	}
	if _, err := server.UpdateGoodsStatus(context.Background(), &proto.UpdateGoodsStatusRequest{Id: id, OnSale: gproto.Bool(false), IsNew: gproto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	doc = readES(id)
	if doc.OnSale || doc.IsNew || doc.Name != update.Name || doc.PriceCents != 3990 {
		t.Fatalf("false status values or unchanged fields were lost: %+v", doc)
	}
	t.Log("PASS AfterUpdate: rename, price, false status values; unrelated fields retained")

	// Inject an ES outage for writes without stopping the user's shared ES.
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"isolated ES outage"}`)
	}))
	defer unavailable.Close()
	failedClient, err := elasticsearch.NewBaseClient(elasticsearch.Config{Addresses: []string{unavailable.URL}, DisableRetry: true})
	if err != nil {
		t.Fatal(err)
	}
	defer failedClient.Close(context.Background())
	global.EsClient = failedClient
	_, updateErr := server.UpdateGoodsStatus(context.Background(), &proto.UpdateGoodsStatusRequest{Id: id, OnSale: gproto.Bool(true)})
	_, deleteErr := server.DeleteGoods(context.Background(), &proto.DeleteGoodsInfo{Id: id})
	failingCreate := gproto.Clone(request).(*proto.CreateGoodsInfo)
	failingCreate.RequestKey = "hooks-create-fail"
	_, createErr := server.CreateGoods(context.Background(), failingCreate)
	global.EsClient = client
	if status.Code(updateErr) != codes.Internal || status.Code(deleteErr) != codes.Internal || status.Code(createErr) != codes.Internal {
		t.Fatalf("ES failure must propagate: update=%v delete=%v create=%v", updateErr, deleteErr, createErr)
	}
	var saved model.Goods
	if err := db.First(&saved, id).Error; err != nil || saved.OnSale {
		t.Fatalf("failed update/delete must roll back MySQL: %+v %v", saved, err)
	}
	var count int64
	if err := db.Model(&model.GoodsCreation{}).Where("request_key = ?", failingCreate.RequestKey).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed creation must roll back its request record: %d %v", count, err)
	}
	if err := db.Model(&model.Goods{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed creation must roll back the product: %d %v", count, err)
	}
	t.Log("PASS ES unavailable: create/update/delete return errors and MySQL changes roll back")

	if _, err := server.UpdateGoodsStatus(context.Background(), &proto.UpdateGoodsStatusRequest{Id: id, OnSale: gproto.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	if !readES(id).OnSale {
		t.Fatal("retry after ES recovery did not update the document")
	}
	if _, err := server.DeleteGoods(context.Background(), &proto.DeleteGoodsInfo{Id: id}); err != nil {
		t.Fatal(err)
	}
	if code, body := esRequest(http.MethodGet, fmt.Sprintf("/%s/_doc/%d", index, id), nil); code != http.StatusNotFound {
		t.Fatalf("AfterDelete retained ES document: %d %s", code, body)
	}
	if err := db.Unscoped().First(&saved, id).Error; err != nil || !saved.DeletedAt.Valid {
		t.Fatalf("MySQL product was not soft-deleted: %v", err)
	}
	t.Log("PASS manual retry after recovery and AfterDelete: MySQL soft-delete removes the ES document")

	request.RequestKey = "hooks-create-002"
	second, err := server.CreateGoods(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := esRequest(http.MethodDelete, fmt.Sprintf("/%s/_doc/%d", index, second.Id), nil); code != http.StatusOK {
		t.Fatalf("prepare already-absent document: %d %s", code, body)
	}
	if _, err := server.DeleteGoods(context.Background(), &proto.DeleteGoodsInfo{Id: second.Id}); err != nil {
		t.Fatalf("an already-absent ES document should allow MySQL deletion: %v", err)
	}
	t.Log("PASS an already-absent ES document does not block deletion")
}
