package handler

import (
	"context"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"grpctest/internal/tracing"
	"math"
	"regexp"
	"sort"
	"time"

	"grpctest/inventory_srv/global"
	"grpctest/inventory_srv/model"
	"grpctest/inventory_srv/model/proto"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InventoryServer struct {
	proto.UnimplementedInventoryServer
}

// Status 0 is an uncommitted claim only. A completed transaction persists 1/2/3.
const (
	stockClaim int32 = iota
	stockReserved
	stockReleased
	stockCancelled
)

var orderNumber = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// Sell records the order and all stock changes in the same transaction.
func (i *InventoryServer) Sell(ctx context.Context, req *proto.SellInfo) (_ *emptypb.Empty, resultErr error) {
	ctx, span := otel.Tracer("grpctest/inventory").Start(ctx, "inventory.sell")
	defer func() { tracing.End(span, resultErr) }()
	if !orderNumber.MatchString(req.GetOrderSn()) {
		return nil, status.Error(codes.InvalidArgument, "订单号无效")
	}
	detail, err := normalizeGoods(req.GoodsInfo)
	if err != nil {
		return nil, err
	}
	dbCtx, dbSpan := otel.Tracer("grpctest/inventory").Start(ctx, "mysql.inventory.reserve", trace.WithSpanKind(trace.SpanKindClient))
	err = global.DB.WithContext(dbCtx).Transaction(func(tx *gorm.DB) error {
		record, err := claimStock(tx, req.OrderSn, detail)
		if err != nil {
			return err
		}
		if record.Status == stockCancelled {
			return status.Error(codes.FailedPrecondition, "该订单已取消，不能扣减库存")
		}
		previous, err := normalizeStored(record.Detail)
		if err != nil {
			return err
		}
		if !equalGoods(previous, detail) {
			return status.Error(codes.AlreadyExists, "同一订单号不能使用不同商品清单")
		}
		switch record.Status {
		case stockReserved:
			return nil
		case stockReleased:
			return status.Error(codes.FailedPrecondition, "该订单库存已归还，不能再次扣减")
		case stockClaim:
		default:
			return status.Error(codes.DataLoss, "库存流水状态无效")
		}
		// The unique order row serializes retries; sorted product IDs avoid lock-order inversion.
		for _, item := range detail {
			result := tx.Model(&model.Inventory{}).Where("goods = ? AND stocks >= ?", item.Goods, item.Num).Updates(map[string]interface{}{
				"stocks": gorm.Expr("stocks - ?", item.Num), "version": gorm.Expr("COALESCE(version,0) + 1"),
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return status.Error(codes.ResourceExhausted, "商品库存不足或不存在")
			}
		}
		return tx.Model(&model.StockSellDetail{}).Where("order_sn = ?", req.OrderSn).Update("status", stockReserved).Error
	})
	tracing.End(dbSpan, err)
	if err != nil {
		return nil, inventoryDBError(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

// Reback uses the stored snapshot, never caller-provided quantities as refund authority.
// A missing order is durably cancelled, fencing a Sell that arrives later.
func (i *InventoryServer) Reback(ctx context.Context, req *proto.SellInfo) (*emptypb.Empty, error) {
	if !orderNumber.MatchString(req.GetOrderSn()) {
		return nil, status.Error(codes.InvalidArgument, "订单号无效")
	}
	var supplied model.GoodsDetailList
	var err error
	if len(req.GoodsInfo) > 0 {
		supplied, err = normalizeGoods(req.GoodsInfo)
		if err != nil {
			return nil, err
		}
	}
	err = global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record, err := claimStock(tx, req.OrderSn, model.GoodsDetailList{})
		if err != nil {
			return err
		}
		if record.Status == stockClaim {
			return tx.Model(&model.StockSellDetail{}).Where("order_sn = ?", req.OrderSn).Update("status", stockCancelled).Error
		}
		if record.Status == stockCancelled {
			return nil
		}
		detail, err := normalizeStored(record.Detail)
		if err != nil {
			return err
		}
		if len(supplied) > 0 && !equalGoods(detail, supplied) {
			return status.Error(codes.AlreadyExists, "归还清单与原扣减记录不一致")
		}
		if record.Status == stockReleased {
			return nil
		}
		if record.Status != stockReserved {
			return status.Error(codes.DataLoss, "库存流水状态无效")
		}
		for _, item := range detail {
			result := tx.Model(&model.Inventory{}).Where("goods = ? AND stocks <= ?", item.Goods, math.MaxInt32-item.Num).Updates(map[string]interface{}{
				"stocks": gorm.Expr("stocks + ?", item.Num), "version": gorm.Expr("COALESCE(version,0) + 1"),
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return status.Error(codes.FailedPrecondition, "库存缺失或归还后数量溢出，需要核对")
			}
		}
		return tx.Model(&model.StockSellDetail{}).Where("order_sn = ?", req.OrderSn).Update("status", stockReleased).Error
	})
	if err != nil {
		return nil, inventoryDBError(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

func claimStock(tx *gorm.DB, order string, detail model.GoodsDetailList) (model.StockSellDetail, error) {
	record := model.StockSellDetail{OrderSn: order, Status: stockClaim, Detail: detail}
	// No-op on conflict, then a locking read. Do not infer ownership from RowsAffected.
	err := tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"order_sn"})}).Create(&record).Error
	if err != nil {
		return record, err
	}
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", order).First(&record).Error
	return record, err
}

func normalizeGoods(items []*proto.GoodsInventoryInfo) (model.GoodsDetailList, error) {
	if len(items) == 0 || len(items) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "商品清单需要 1 到 1000 项")
	}
	counts := map[int32]int64{}
	for _, item := range items {
		if item.GetGoodsId() <= 0 || item.GetNum() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "商品 ID 和数量必须为正数")
		}
		counts[item.GoodsId] += int64(item.Num)
		if counts[item.GoodsId] > math.MaxInt32 {
			return nil, status.Error(codes.InvalidArgument, "商品数量溢出")
		}
	}
	result := make(model.GoodsDetailList, 0, len(counts))
	for id, n := range counts {
		result = append(result, model.GoodsDetail{Goods: id, Num: int32(n)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Goods < result[j].Goods })
	return result, nil
}
func normalizeStored(items model.GoodsDetailList) (model.GoodsDetailList, error) {
	wire := make([]*proto.GoodsInventoryInfo, 0, len(items))
	for _, item := range items {
		wire = append(wire, &proto.GoodsInventoryInfo{GoodsId: item.Goods, Num: item.Num})
	}
	result, err := normalizeGoods(wire)
	if err != nil {
		return nil, status.Error(codes.DataLoss, "库存流水清单无效")
	}
	return result, nil
}
func equalGoods(a, b model.GoodsDetailList) bool {
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n] != b[n] {
			return false
		}
	}
	return true
}

func (i *InventoryServer) GetStockOperation(ctx context.Context, req *proto.StockOperationRequest) (*proto.StockOperationResponse, error) {
	if !orderNumber.MatchString(req.GetOrderSn()) {
		return nil, status.Error(codes.InvalidArgument, "订单号无效")
	}
	var record model.StockSellDetail
	if err := global.DB.WithContext(ctx).Where("order_sn = ?", req.OrderSn).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "尚无已提交的库存处理记录")
		}
		return nil, inventoryDBError(ctx, err)
	}
	if record.Status < stockReserved || record.Status > stockCancelled {
		return nil, status.Error(codes.DataLoss, "库存流水状态无效")
	}
	result := &proto.StockOperationResponse{OrderSn: record.OrderSn, State: proto.StockOperationState(record.Status)}
	if record.Status != stockCancelled {
		detail, err := normalizeStored(record.Detail)
		if err != nil {
			return nil, err
		}
		for _, item := range detail {
			result.GoodsInfo = append(result.GoodsInfo, &proto.GoodsInventoryInfo{GoodsId: item.Goods, Num: item.Num})
		}
	}
	return result, nil
}

// AutoReback shares the RPC refund transaction and acknowledges the entire batch only after commit.
func AutoReback(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for _, msg := range msgs {
		var order struct{ OrderSn string }
		if msg == nil {
			return consumer.ConsumeRetryLater, nil
		}
		if err := json.Unmarshal(msg.Body, &order); err != nil || !orderNumber.MatchString(order.OrderSn) {
			zap.S().Errorw("返库消息格式无效，保留重试", "message_id", msg.MsgId)
			return consumer.ConsumeRetryLater, nil
		}
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := (&InventoryServer{}).Reback(requestCtx, &proto.SellInfo{OrderSn: order.OrderSn})
		cancel()
		if err != nil {
			return consumer.ConsumeRetryLater, nil
		}
	}
	return consumer.ConsumeSuccess, nil
}
