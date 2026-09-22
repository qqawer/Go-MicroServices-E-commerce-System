//go:build integration

package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm/clause"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"gorm.io/gorm"
	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/handler"
	"grpctest/inventory_srv/model"
	"grpctest/inventory_srv/model/proto"
	"grpctest/tests/integration/testdb"
)

func transactionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t, "grpctest_inventory_srv")
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	return db
}
func stockFixture(t *testing.T, db *gorm.DB, order string, goods ...int32) {
	t.Helper()
	detail := model.GoodsDetailList{}
	for _, id := range goods {
		if err := db.Create(&model.Inventory{Goods: id, Stocks: 98}).Error; err != nil {
			t.Fatal(err)
		}
		detail = append(detail, model.GoodsDetail{Goods: id, Num: 2})
	}
	if err := db.Create(&model.StockSellDetail{OrderSn: order, Status: 1, Detail: detail}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM stockselldetail WHERE order_sn = ?", order)
		db.Unscoped().Where("goods IN ?", goods).Delete(&model.Inventory{})
	})
}
func assertStock(t *testing.T, db *gorm.DB, id, want int32) {
	t.Helper()
	var inv model.Inventory
	if err := db.Where("goods = ?", id).First(&inv).Error; err != nil {
		t.Fatal(err)
	}
	if inv.Stocks != want {
		t.Fatalf("goods %d stock=%d want=%d", id, inv.Stocks, want)
	}
}
func TestRebackDoesNotRefundTwice(t *testing.T) {
	db := transactionDB(t)
	stockFixture(t, db, "refund-twice", 800001)
	req := &proto.SellInfo{OrderSn: "refund-twice", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 800001, Num: 2}}}
	for n := 0; n < 2; n++ {
		if _, err := (&handler.InventoryServer{}).Reback(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	assertStock(t, db, 800001, 100)
}
func TestAutoRebackReturnsAllGoods(t *testing.T) {
	db := transactionDB(t)
	stockFixture(t, db, "refund-multiple", 800002, 800003)
	result, err := handler.AutoReback(context.Background(), &primitive.MessageExt{Message: primitive.Message{Body: []byte(`{"OrderSn":"refund-multiple"}`)}})
	if err != nil || result != consumer.ConsumeSuccess {
		t.Fatalf("multi-product refund was not acknowledged: %v %v", result, err)
	}
	assertStock(t, db, 800002, 100)
	assertStock(t, db, 800003, 100)
}
func TestAutoRebackQueryFailureIsNotAcknowledged(t *testing.T) {
	db := transactionDB(t)
	name := "test:refund_query_error"
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) { tx.AddError(errors.New("injected query failure")) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
	result, _ := handler.AutoReback(context.Background(), &primitive.MessageExt{Message: primitive.Message{Body: []byte(fmt.Sprintf(`{"OrderSn":%q}`, "refund-db-failure"))}})
	if result != consumer.ConsumeRetryLater {
		t.Fatalf("database failure was acknowledged: %v", result)
	}
}

func TestStockOperationsAreAtomicAndQueryable(t *testing.T) {
	db := transactionDB(t)
	for _, id := range []int32{800010, 800011} {
		if err := db.Create(&model.Inventory{Goods: id, Stocks: 100}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM stockselldetail WHERE order_sn LIKE 'atomic-%'")
		db.Unscoped().Where("goods IN ?", []int32{800010, 800011}).Delete(&model.Inventory{})
	})
	ctx, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterInventoryServer(s, &handler.InventoryServer{}) })
	client := proto.NewInventoryClient(conn)
	req := &proto.SellInfo{OrderSn: "atomic-insufficient", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 800010, Num: 2}, {GoodsId: 800011, Num: 101}}}
	if _, err := client.Sell(ctx, req); status.Code(err) != codes.ResourceExhausted {
		t.Fatal("want insufficient stock", err)
	}
	assertStock(t, db, 800010, 100)
	assertStock(t, db, 800011, 100)
	if _, err := client.GetStockOperation(ctx, &proto.StockOperationRequest{OrderSn: req.OrderSn}); status.Code(err) != codes.NotFound {
		t.Fatal("failed transaction left a ledger", err)
	}
	req.OrderSn = "atomic-success"
	req.GoodsInfo[1].Num = 3
	if _, err := client.Sell(ctx, req); err != nil {
		t.Fatal(err)
	}
	// Reordering and merging duplicate product lines describe the same request.
	retry := &proto.SellInfo{OrderSn: req.OrderSn, GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 800011, Num: 1}, {GoodsId: 800010, Num: 2}, {GoodsId: 800011, Num: 2}}}
	if _, err := client.Sell(ctx, retry); err != nil {
		t.Fatal(err)
	}
	assertStock(t, db, 800010, 98)
	assertStock(t, db, 800011, 97)
	result, err := client.GetStockOperation(ctx, &proto.StockOperationRequest{OrderSn: req.OrderSn})
	if err != nil || result.GetState() != proto.StockOperationState_STOCK_RESERVED || len(result.GoodsInfo) != 2 {
		t.Fatal(result, err)
	}
	retry.GoodsInfo[0].Num = 2
	if _, err := client.Sell(ctx, retry); status.Code(err) != codes.AlreadyExists {
		t.Fatal("changed payload accepted", err)
	}
	if _, err := client.Reback(ctx, retry); status.Code(err) != codes.AlreadyExists {
		t.Fatal("forged refund amount accepted", err)
	}
	if _, err := client.Reback(ctx, &proto.SellInfo{OrderSn: req.OrderSn}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Sell(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("released order deducted again", err)
	}
	assertStock(t, db, 800010, 100)
	assertStock(t, db, 800011, 100)
	result, err = client.GetStockOperation(ctx, &proto.StockOperationRequest{OrderSn: req.OrderSn})
	if err != nil || result.GetState() != proto.StockOperationState_STOCK_RELEASED {
		t.Fatal(result, err)
	}
	cancelReq := &proto.SellInfo{OrderSn: "atomic-cancel-first"}
	if _, err := client.Reback(ctx, cancelReq); err != nil {
		t.Fatal(err)
	}
	cancelReq.GoodsInfo = req.GoodsInfo
	if _, err := client.Sell(ctx, cancelReq); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("late Sell bypassed cancellation", err)
	}
	result, err = client.GetStockOperation(ctx, &proto.StockOperationRequest{OrderSn: cancelReq.OrderSn})
	if err != nil || result.GetState() != proto.StockOperationState_STOCK_CANCELLED {
		t.Fatal(result, err)
	}
}

func TestStockDatabaseWriteFailureRollsBackAllProducts(t *testing.T) {
	db := transactionDB(t)
	stockFixture(t, db, "stock-write-error", 800020, 800021)
	name := "test:stock_write_error"
	db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "inventory" {
			var values []interface{}
			if where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where); ok {
				for _, expr := range where.Exprs {
					if e, ok := expr.(clause.Expr); ok {
						values = append(values, e.Vars...)
					}
				}
			}
			for _, value := range values {
				if value == int32(800021) {
					tx.AddError(errors.New("injected second product write failure"))
				}
			}
		}
	})
	defer db.Callback().Update().Remove(name)
	result, _ := handler.AutoReback(context.Background(), &primitive.MessageExt{Message: primitive.Message{Body: []byte(`{"OrderSn":"stock-write-error"}`)}})
	if result != consumer.ConsumeRetryLater {
		t.Fatal("failed multi-product refund acknowledged")
	}
	assertStock(t, db, 800020, 98)
	assertStock(t, db, 800021, 98)
	var record model.StockSellDetail
	if err := db.Where("order_sn = ?", "stock-write-error").First(&record).Error; err != nil || record.Status != 1 {
		t.Fatal("failed refund changed ledger", record, err)
	}
}

type lostStockReply struct {
	proto.UnimplementedInventoryServer
	drop atomic.Bool
}

func (s *lostStockReply) Sell(ctx context.Context, req *proto.SellInfo) (*emptypb.Empty, error) {
	result, err := (&handler.InventoryServer{}).Sell(ctx, req)
	if err == nil && s.drop.CompareAndSwap(false, true) {
		return nil, status.Error(codes.DeadlineExceeded, "committed but response lost")
	}
	return result, err
}
func (s *lostStockReply) GetStockOperation(ctx context.Context, req *proto.StockOperationRequest) (*proto.StockOperationResponse, error) {
	return (&handler.InventoryServer{}).GetStockOperation(ctx, req)
}
func TestStockCommittedButReplyLostCanBeQueriedAndRetried(t *testing.T) {
	db := transactionDB(t)
	db.Create(&model.Inventory{Goods: 800030, Stocks: 100})
	t.Cleanup(func() {
		db.Exec("DELETE FROM stockselldetail WHERE order_sn = 'stock-reply-lost'")
		db.Unscoped().Where("goods = ?", 800030).Delete(&model.Inventory{})
	})
	ctx, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterInventoryServer(s, &lostStockReply{}) })
	client := proto.NewInventoryClient(conn)
	req := &proto.SellInfo{OrderSn: "stock-reply-lost", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 800030, Num: 2}}}
	if _, err := client.Sell(ctx, req); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	record, err := client.GetStockOperation(ctx, &proto.StockOperationRequest{OrderSn: req.OrderSn})
	if err != nil || record.GetState() != proto.StockOperationState_STOCK_RESERVED {
		t.Fatal(record, err)
	}
	if _, err := client.Sell(ctx, req); err != nil {
		t.Fatal(err)
	}
	assertStock(t, db, 800030, 98)
}

func parallelStock(t *testing.T, count, workers int, call func(int) error) {
	t.Helper()
	jobs := make(chan int)
	results := make(chan error, count)
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				results <- call(job)
			}
		}()
	}
	for n := 0; n < count; n++ {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
}
func TestStockCompetitionAndRepeatedDelivery(t *testing.T) {
	db := transactionDB(t)
	t.Cleanup(func() {
		db.Exec("DELETE FROM stockselldetail WHERE order_sn LIKE 'stress-%'")
		db.Unscoped().Where("goods BETWEEN ? AND ?", 800100, 800110).Delete(&model.Inventory{})
	})
	_, conn := testdb.Client(t, func(s *grpc.Server) { proto.RegisterInventoryServer(s, &handler.InventoryServer{}) })
	client := proto.NewInventoryClient(conn)
	for round := 0; round < 3; round++ {
		id := int32(800100 + round)
		if err := db.Create(&model.Inventory{Goods: id, Stocks: 100}).Error; err != nil {
			t.Fatal(err)
		}
		var sold atomic.Int32
		parallelStock(t, 1000, 100, func(n int) error {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := client.Sell(ctx, &proto.SellInfo{OrderSn: fmt.Sprintf("stress-%d-%d", round, n), GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: id, Num: 1}}})
			if err == nil {
				sold.Add(1)
				return nil
			}
			if status.Code(err) == codes.ResourceExhausted {
				return nil
			}
			return err
		})
		if sold.Load() != 100 {
			t.Fatalf("round %d sold=%d want=100", round, sold.Load())
		}
		assertStock(t, db, id, 0)
	}
	id := int32(800110)
	if err := db.Create(&model.Inventory{Goods: id, Stocks: 100}).Error; err != nil {
		t.Fatal(err)
	}
	parallelStock(t, 100, 30, func(_ int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := client.Sell(ctx, &proto.SellInfo{OrderSn: "stress-same-order", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: id, Num: 2}}})
		return err
	})
	assertStock(t, db, id, 98)
	parallelStock(t, 1000, 100, func(_ int) error {
		result, err := handler.AutoReback(context.Background(), &primitive.MessageExt{Message: primitive.Message{Body: []byte(`{"OrderSn":"stress-same-order"}`)}})
		if err != nil {
			return err
		}
		if result != consumer.ConsumeSuccess {
			return errors.New("duplicate refund requested retry")
		}
		return nil
	})
	assertStock(t, db, id, 100)
}

func TestAutoRebackProcessesWholeBatch(t *testing.T) {
	db := transactionDB(t)
	stockFixture(t, db, "batch-refund-a", 800040)
	stockFixture(t, db, "batch-refund-b", 800041)
	msgs := []*primitive.MessageExt{}
	for _, number := range []string{"batch-refund-a", "batch-refund-b"} {
		msgs = append(msgs, &primitive.MessageExt{Message: primitive.Message{Body: []byte(fmt.Sprintf(`{"OrderSn":%q}`, number))}})
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := handler.AutoReback(context.Background(), msgs...)
		if err != nil || result != consumer.ConsumeSuccess {
			t.Fatal(result, err)
		}
	}
	assertStock(t, db, 800040, 100)
	assertStock(t, db, 800041, 100)
}

func TestStockCommitFailureAndLostAcknowledgment(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%v", after), func(t *testing.T) {
			db := transactionDB(t)
			if err := db.Create(&model.Inventory{Goods: 800050, Stocks: 100}).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Exec("DELETE FROM stockselldetail WHERE order_sn = 'stock-commit-fault'")
				db.Unscoped().Where("goods = ?", 800050).Delete(&model.Inventory{})
			})
			testdb.FailCommit(t, db, 1, after)
			req := &proto.SellInfo{OrderSn: "stock-commit-fault", GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 800050, Num: 2}}}
			server := &handler.InventoryServer{}
			if _, err := server.Sell(context.Background(), req); status.Code(err) != codes.Internal {
				t.Fatal("commit error was swallowed", err)
			}
			want := int32(100)
			if after {
				want = 98
			}
			assertStock(t, db, 800050, want)
			record, err := server.GetStockOperation(context.Background(), &proto.StockOperationRequest{OrderSn: req.OrderSn})
			if after {
				if err != nil || record.GetState() != proto.StockOperationState_STOCK_RESERVED {
					t.Fatal(record, err)
				}
			} else if status.Code(err) != codes.NotFound {
				t.Fatal("failed commit left a record", record, err)
			}
			if _, err := server.Sell(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			assertStock(t, db, 800050, 98)
		})
	}
}
func TestStockCancellationRacingWithDeduction(t *testing.T) {
	db := transactionDB(t)
	if err := db.Create(&model.Inventory{Goods: 800060, Stocks: 100}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM stockselldetail WHERE order_sn LIKE 'cancel-race-%'")
		db.Unscoped().Where("goods = ?", 800060).Delete(&model.Inventory{})
	})
	for n := 0; n < 30; n++ {
		req := &proto.SellInfo{OrderSn: fmt.Sprintf("cancel-race-%d", n), GoodsInfo: []*proto.GoodsInventoryInfo{{GoodsId: 800060, Num: 2}}}
		start := make(chan struct{})
		sold := make(chan error, 1)
		released := make(chan error, 1)
		go func() { <-start; _, err := (&handler.InventoryServer{}).Sell(context.Background(), req); sold <- err }()
		go func() {
			<-start
			_, err := (&handler.InventoryServer{}).Reback(context.Background(), &proto.SellInfo{OrderSn: req.OrderSn})
			released <- err
		}()
		close(start)
		if err := <-sold; err != nil && status.Code(err) != codes.FailedPrecondition {
			t.Fatal(err)
		}
		if err := <-released; err != nil {
			t.Fatal(err)
		}
		assertStock(t, db, 800060, 100)
	}
}
