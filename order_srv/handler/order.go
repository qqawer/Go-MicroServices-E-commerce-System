package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"math"
	"time"

	"github.com/opentracing/opentracing-go"

	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/golang/protobuf/ptypes/empty"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GenerationSn 生成订单编号
func GenerationSn(_ int32) string { return uuid.NewString() }

// Paginate 将数据进行分页
func Paginate(page, pageSize int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if page == 0 {
			page = 1
		}

		switch {
		case pageSize > 100:
			pageSize = 100
		case pageSize <= 0:
			pageSize = 10
		}

		offset := (page - 1) * pageSize
		return db.Offset(offset).Limit(pageSize)
	}
}

// OrderServer 订单服务
type OrderServer struct {
	proto.UnimplementedOrderServer
}

var _ proto.OrderServer = (*OrderServer)(nil)

// CartItemList 获取当前用户的购物车，零值用户不能省略查询条件。
func (O *OrderServer) CartItemList(ctx context.Context, req *proto.UserInfo) (*proto.CartItemListResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户 ID 必须为正数")
	}
	var carts []model.ShoppingCart
	if err := global.DB.WithContext(ctx).Where("user = ?", req.Id).Find(&carts).Error; err != nil {
		return nil, cartError(ctx, err)
	}
	rsp := &proto.CartItemListResponse{Total: int32(len(carts))}
	for _, cart := range carts {
		rsp.Data = append(rsp.Data, cartResponse(cart))
	}
	return rsp, nil
}

// CreateCarItem 新增商品到购物车；已有记录累加数量并保留勾选状态。
func (O *OrderServer) CreateCarItem(ctx context.Context, req *proto.CartItemRequest) (*proto.ShopCartInfoResponse, error) {
	if req.GetUserId() <= 0 || req.GetGoodsId() <= 0 || req.GetNums() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户、商品 ID 和数量必须为正数")
	}
	var cart model.ShoppingCart
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("goods = ? AND user = ?", req.GoodsId, req.UserId).First(&cart).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			cart = model.ShoppingCart{User: req.UserId, Goods: req.GoodsId, Nums: req.GetNums()}
			return tx.Create(&cart).Error
		}
		if err != nil {
			return err
		}
		if int64(cart.Nums)+int64(req.GetNums()) > math.MaxInt32 {
			return status.Error(codes.InvalidArgument, "购物车数量超出范围")
		}
		cart.Nums += req.GetNums()
		return tx.Model(&model.ShoppingCart{}).Where("id = ? AND user = ?", cart.ID, req.UserId).Update("nums", cart.Nums).Error
	})
	if err != nil {
		return nil, cartError(ctx, err)
	}
	return cartResponse(cart), nil
}

// UpdateCartItem 按购物车行 ID 和当前用户定位，只更新明确提供的字段。
func (O *OrderServer) UpdateCartItem(ctx context.Context, req *proto.CartItemRequest) (*empty.Empty, error) {
	if req.GetId() <= 0 || req.GetUserId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "购物车和用户 ID 必须为正数")
	}
	if req.Nums == nil && req.Checked == nil || req.Nums != nil && req.GetNums() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "必须提供正数数量或勾选状态")
	}
	changes := map[string]interface{}{}
	if req.Nums != nil {
		changes["nums"] = req.GetNums()
	}
	if req.Checked != nil {
		changes["checked"] = req.GetChecked()
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先确认归属并锁定行，使值未变化的更新也能成功；不使用 Save 的插入回退。
		var cart model.ShoppingCart
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user = ?", req.Id, req.UserId).First(&cart).Error; err != nil {
			return err
		}
		return tx.Model(&model.ShoppingCart{}).Where("id = ? AND user = ?", req.Id, req.UserId).Updates(changes).Error
	})
	if err != nil {
		return nil, cartError(ctx, err)
	}
	return &empty.Empty{}, nil
}

// DeleteCartItem 删除当前用户拥有的购物车行。
func (O *OrderServer) DeleteCartItem(ctx context.Context, req *proto.CartItemRequest) (*empty.Empty, error) {
	if req.GetId() <= 0 || req.GetUserId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "购物车和用户 ID 必须为正数")
	}
	result := global.DB.WithContext(ctx).Where("id = ? AND user = ?", req.Id, req.UserId).Delete(&model.ShoppingCart{})
	if result.Error != nil {
		return nil, cartError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, status.Error(codes.NotFound, "购物车记录不存在")
	}
	return &empty.Empty{}, nil
}

func cartResponse(cart model.ShoppingCart) *proto.ShopCartInfoResponse {
	return &proto.ShopCartInfoResponse{Id: cart.ID, UserId: cart.User, GoodsId: cart.Goods, Nums: cart.Nums, Checked: cart.Checked}
}

func cartError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.NotFound, "购物车记录不存在")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	zap.S().Errorw("购物车数据库操作失败", "error", err)
	return status.Error(codes.Internal, "购物车数据库操作失败")
}

// OrderListener 发送消息 可靠消息传送，基于rocketmq的延时机制，对订单支付状态检查以及及时归还库存
type OrderListener struct {
	Code        codes.Code      //返回状态码
	Detail      string          //返回状态描述
	ID          int32           //订单id
	OrderAmount float32         //订单总金额
	Ctx         context.Context //上下文数据
}

// ExecuteLocalTransaction  When send transactional prepare(half) message succeed, this method will be invoked to execute local transaction.
func (o *OrderListener) ExecuteLocalTransaction(msg *primitive.Message) primitive.LocalTransactionState {
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	span, ctx := opentracing.StartSpanFromContext(ctx, "order_transaction")
	defer span.Finish()
	o.Code = codes.Internal
	var order model.OrderInfo
	if msg == nil || json.Unmarshal(msg.Body, &order) != nil || !validOrderSN.MatchString(order.OrderSn) || order.User <= 0 {
		o.Code = codes.InvalidArgument
		o.Detail = "订单消息无效"
		return primitive.UnkonwnState
	}
	state, err := orderDecision(ctx, order.OrderSn, false)
	if err != nil {
		o.Detail = "订单事务记录失败"
		return primitive.UnkonwnState
	}
	if state == model.OrderCancelled {
		o.Code = codes.Aborted
		o.Detail = "订单已取消"
		return primitive.CommitMessageState
	}
	if state == model.OrderCommitted {
		if err := global.DB.WithContext(ctx).Where("order_sn = ?", order.OrderSn).First(&order).Error; err != nil {
			return primitive.UnkonwnState
		}
		o.Code = codes.OK
		o.ID = order.ID
		o.OrderAmount = order.OrderMount
		return primitive.RollbackMessageState
	}
	fail := func(code codes.Code, detail string) primitive.LocalTransactionState {
		o.Code = code
		o.Detail = detail
		// The caller may have timed out while Sell committed. Persist cancellation using
		// a separate bounded context, so the compensation also fences a late Sell.
		decisionCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		return cancelOrderMessage(decisionCtx, order.OrderSn)
	}
	var record model.OrderTransaction
	if err := global.DB.WithContext(ctx).Where("order_sn = ?", order.OrderSn).First(&record).Error; err != nil {
		return primitive.UnkonwnState
	}
	var snapshot creationSnapshot
	if record.Snapshot == nil || json.Unmarshal([]byte(*record.Snapshot), &snapshot) != nil || snapshot.Order.OrderSn != order.OrderSn || snapshot.Order.User != order.User || len(snapshot.Stock) == 0 {
		return fail(codes.FailedPrecondition, "缺少持久下单快照")
	}
	if record.ExpiresAt != nil && !time.Now().Before(*record.ExpiresAt) {
		return fail(codes.Aborted, "下单请求已过期")
	}
	order = snapshot.Order
	cart, orderGoods, stock := snapshot.Cart, snapshot.Goods, snapshot.Stock
	if _, err := global.InventorySrvClient.Sell(ctx, &proto.SellInfo{OrderSn: order.OrderSn, GoodsInfo: stock}); err != nil {
		// Even InvalidArgument/Unavailable is a decision to abort this order; never
		// infer that nothing committed from an RPC error and discard compensation.
		return fail(status.Code(err), "扣减库存未确认，订单取消并安排返库")
	}
	order.Status = "WAIT_BUYER_PAY"
	err = global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var decision model.OrderTransaction
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", order.OrderSn).First(&decision).Error; err != nil {
			return err
		}
		if decision.State == model.OrderCancelled {
			return status.Error(codes.Aborted, "订单已被回查取消")
		}
		if decision.State == model.OrderCommitted {
			return tx.Where("order_sn = ?", order.OrderSn).First(&order).Error
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		for n := range orderGoods {
			orderGoods[n].Order = order.ID
		}
		if err := tx.CreateInBatches(&orderGoods, 100).Error; err != nil {
			return err
		}
		// Preserve carts edited while RPCs were in flight instead of deleting new choices.
		for _, item := range cart {
			if err := tx.Where("id = ? AND user = ? AND nums = ? AND checked = ?", item.ID, order.User, item.Nums, true).Delete(&model.ShoppingCart{}).Error; err != nil {
				return err
			}
		}
		if err := addOrderEvent(tx, order.OrderSn, "order_timeout", timeoutDelay(record.ExpiresAt)); err != nil {
			return err
		}
		return tx.Model(&model.OrderTransaction{}).Where("order_sn = ?", order.OrderSn).Update("state", model.OrderCommitted).Error
	})
	if err != nil {
		decisionCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		// Commit acknowledgment may be lost. The durable decision determines whether to compensate.
		state, e := orderDecision(decisionCtx, order.OrderSn, true)
		if e != nil {
			o.Detail = "订单提交结果待确认"
			return primitive.UnkonwnState
		}
		if state != model.OrderCommitted {
			o.Detail = "订单创建失败，已安排返库"
			return primitive.CommitMessageState
		}
		if e := global.DB.WithContext(decisionCtx).Where("order_sn = ?", order.OrderSn).First(&order).Error; e != nil {
			return primitive.UnkonwnState
		}
	}
	o.ID = order.ID
	o.OrderAmount = order.OrderMount
	o.Code = codes.OK
	if err := publishOrderEvent(ctx, order.OrderSn, "order_timeout"); err != nil {
		zap.S().Errorw("订单已创建，延时消息待重发", "order_sn", order.OrderSn, "error", err)
	}
	return primitive.RollbackMessageState
}

func (o *OrderListener) CheckLocalTransaction(msg *primitive.MessageExt) primitive.LocalTransactionState {
	var order struct{ OrderSn string }
	if msg == nil || json.Unmarshal(msg.Body, &order) != nil || !validOrderSN.MatchString(order.OrderSn) {
		return primitive.UnkonwnState
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Checking a preparing/missing operation durably cancels it. The execution path
	// locks the same decision before writing an order, so a late commit cannot resurrect it.
	return cancelOrderMessage(ctx, order.OrderSn)
}

// OrderList 订单列表
func (O *OrderServer) OrderList(ctx context.Context, req *proto.OrderFilterRequest) (*proto.OrderListResponse, error) {
	var orders []model.OrderInfo
	var Rsp proto.OrderListResponse
	//如果没有userid 就是默认值0，而gorm正好会忽略0值
	var total int64
	global.DB.Where(&model.OrderInfo{User: req.UserId}).Find(&model.OrderInfo{}).Count(&total)
	Rsp.Total = int32(total)
	global.DB.Scopes(Paginate(int(req.Pages), int(req.PagePerNums))).Where(&model.OrderInfo{User: req.UserId}).Find(&orders)
	for _, order := range orders {
		Rsp.Data = append(Rsp.Data, orderResponse(order))
	}
	return &Rsp, nil
}

// OrderDetail 订单详情
func (O *OrderServer) OrderDetail(ctx context.Context, req *proto.OrderRequest) (*proto.OrderInfoDetailResponse, error) {
	var order model.OrderInfo
	var Rsp proto.OrderInfoDetailResponse
	if result := global.DB.Where(&model.OrderInfo{BaseModel: model.BaseModel{ID: req.Id}, User: req.UserId}).First(&order); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "订单记录不存在")
	}

	OrderInfo := *orderResponse(order)

	OrderInfo.Id = order.ID
	OrderInfo.UserId = order.User
	OrderInfo.OrderSn = order.OrderSn
	OrderInfo.PayType = order.PayType
	OrderInfo.Status = order.Status
	OrderInfo.Post = order.Post
	OrderInfo.Total = order.OrderMount
	OrderInfo.Address = order.Address
	OrderInfo.Name = order.SignerName
	OrderInfo.Mobile = order.SingerMobile
	OrderInfo.AddTime = fmt.Sprintf("%d-%d-%d %d:%d:%d", order.CreatedAt.Year(), order.CreatedAt.Month(), order.CreatedAt.Day(), order.CreatedAt.Hour(), order.CreatedAt.Minute(), order.CreatedAt.Second())

	Rsp.OrderInfo = &OrderInfo

	var GoodsList []model.OrderGoods
	if result := global.DB.Where(&model.OrderGoods{Order: order.ID}).Find(&GoodsList); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "商品不存在")
	}
	for _, goods := range GoodsList {
		Rsp.Goods = append(Rsp.Goods, &proto.OrderItemResponse{
			Id:         goods.ID,
			OrderId:    goods.Order,
			GoodsId:    goods.Goods,
			GoodsName:  goods.GoodsName,
			GoodsImage: goods.GoodsImage,
			GoodsPrice: goods.GoodsPrice,
			Nums:       goods.Nums,
		})
	}

	return &Rsp, nil
}

// UpdateOrderStatus 更新订单状态
func (O *OrderServer) UpdateOrderStatus(ctx context.Context, req *proto.OrderStatus) (*empty.Empty, error) {
	if !validOrderSN.MatchString(req.GetOrderSn()) {
		return nil, status.Error(codes.InvalidArgument, "订单号无效")
	}
	if req.Status == "TRADE_CLOSED" {
		_, err := closeOrder(ctx, req.OrderSn)
		if err != nil {
			return nil, status.Error(codes.Internal, "订单关闭结果待确认")
		}
		// The persisted refund intent is handled by the dispatcher; do not couple it
		// to the HTTP/payment caller's lifetime.
		return &empty.Empty{}, nil
	}
	if req.Status != "TRADE_SUCCESS" && req.Status != "TRADE_FINISHED" && req.Status != "WAIT_BUYER_PAY" {
		return nil, status.Error(codes.InvalidArgument, "订单状态无效")
	}
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order model.OrderInfo
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_sn = ?", req.OrderSn).First(&order).Error; err != nil {
			return err
		}
		if order.PayType == "demo" {
			return status.Error(codes.FailedPrecondition, "演示订单不支持付款")
		}
		if order.Status == req.Status {
			return nil
		}
		switch order.Status {
		case "", "PAYING", "WAIT_BUYER_PAY":
			if req.Status != "TRADE_SUCCESS" && req.Status != "WAIT_BUYER_PAY" {
				return status.Error(codes.FailedPrecondition, "订单状态转换无效")
			}
		case "TRADE_SUCCESS":
			if req.Status != "TRADE_FINISHED" {
				return status.Error(codes.FailedPrecondition, "已支付订单不能回退")
			}
		default:
			return status.Error(codes.FailedPrecondition, "订单已结束，需要核对支付结果")
		}
		return tx.Model(&model.OrderInfo{}).Where("id = ?", order.ID).Update("status", req.Status).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.NotFound, "订单不存在")
		}
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Error(codes.Internal, "订单状态更新失败")
	}
	return &empty.Empty{}, nil
}

func TimeOutReback(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for _, msg := range msgs {
		var order struct{ OrderSn string }
		if msg == nil || json.Unmarshal(msg.Body, &order) != nil || !validOrderSN.MatchString(order.OrderSn) {
			return consumer.ConsumeRetryLater, nil
		}
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		refund, err := closeOrder(requestCtx, order.OrderSn)
		if err == nil && refund {
			err = publishOrderEvent(requestCtx, order.OrderSn, "order-reback")
		}
		cancel()
		if err != nil {
			return consumer.ConsumeRetryLater, nil
		}
	}
	return consumer.ConsumeSuccess, nil
}
