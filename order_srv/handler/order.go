package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/opentracing/opentracing-go"

	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/golang/protobuf/ptypes/empty"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GenerationSn 生成订单编号
func GenerationSn(userId int32) string {
	rand.Seed(time.Now().UnixNano())
	now := time.Now()
	//年月日时分秒 + 用户id + 两位随机数
	orderSn := fmt.Sprintf("%d%d%d%d%d%d%d%d", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), userId, rand.Intn(90)+10)
	return orderSn
}

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
func (o *OrderListener) ExecuteLocalTransaction(msgs *primitive.Message) primitive.LocalTransactionState {
	//执行本地事务
	//1. 从购物车获取商品信息
	//2. 去查询商品服务(跨服务)
	//3. 调用库存服务扣减库存(跨服务)
	//4.订单的基本信息表 和订单的商品信息表
	//5.从购物车中删除已购买记录

	//接收上下文数据链路追踪
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	parentSpan, _ := opentracing.StartSpanFromContext(ctx, "order_transaction")
	defer parentSpan.Finish()

	fmt.Println("开始执行本地事务")
	var OrderInfo model.OrderInfo
	_ = json.Unmarshal(msgs.Body, &OrderInfo)

	//1. 从购物车获取商品信息
	var goodsIds []int32
	var shopCartList []model.ShoppingCart
	goodsNumsMap := make(map[int32]int32)

	//开始追踪
	shoppingTracer := opentracing.GlobalTracer().StartSpan("select_shopping_cart", opentracing.ChildOf(parentSpan.Context()))

	if result := global.DB.Where(&model.ShoppingCart{User: OrderInfo.User, Checked: true}).Find(&shopCartList); result.RowsAffected == 0 {
		//将数据返回至实现者结构体
		o.Code = codes.InvalidArgument
		o.Detail = "没有选择结算的商品"
		return primitive.RollbackMessageState
	}

	//结束当前追踪
	shoppingTracer.Finish()

	for _, shopCart := range shopCartList {
		goodsIds = append(goodsIds, shopCart.Goods)
		goodsNumsMap[shopCart.Goods] = shopCart.Nums
	}

	//开始追踪
	queryGoodsSpan := opentracing.GlobalTracer().StartSpan("query_goods", opentracing.ChildOf(parentSpan.Context()))

	//2. 去查询商品服务(跨服务),批量查询
	Goods, err := global.GoodsSrvClient.BatchGetGoods(context.Background(), &proto.BatchGoodsIdInfo{
		Id: goodsIds,
	})
	if err != nil {
		o.Code = codes.Internal
		o.Detail = "批量查询商品失败"

		return primitive.RollbackMessageState
	}

	queryGoodsSpan.Finish()

	//总价
	var orderAmount float32
	var orderGoods []*model.OrderGoods
	var GoodsInfo []*proto.GoodsInventoryInfo
	for _, goods := range Goods.Data {
		orderAmount += goods.ShopPrice * float32(goodsNumsMap[goods.Id])
		orderGoods = append(orderGoods, &model.OrderGoods{
			Goods:      goods.Id,
			GoodsName:  goods.Name,
			GoodsPrice: goods.ShopPrice,
			GoodsImage: goods.GoodsFrontImage,
			Nums:       goodsNumsMap[goods.Id],
		})
		GoodsInfo = append(GoodsInfo, &proto.GoodsInventoryInfo{
			GoodsId: goods.Id,
			Num:     goodsNumsMap[goods.Id],
		})
	}

	//开始追踪
	queryInvSpan := opentracing.GlobalTracer().StartSpan("query_inventory", opentracing.ChildOf(parentSpan.Context()))

	//3. 调用库存服务扣减库存(跨服务)
	_, err = global.InventorySrvClient.Sell(context.Background(), &proto.SellInfo{
		GoodsInfo: GoodsInfo,
		OrderSn:   OrderInfo.OrderSn,
	})
	if err != nil {
		o.Code = codes.ResourceExhausted
		o.Detail = "扣减库存失败"
		zap.S().Errorf("库存扣减失败：%s", err)
		return primitive.RollbackMessageState
	}

	queryInvSpan.Finish()

	//需要事务逻辑
	tx := global.DB.Begin()

	OrderInfo.OrderMount = orderAmount

	//开始追踪
	querySaveSpan := opentracing.GlobalTracer().StartSpan("query_Save", opentracing.ChildOf(parentSpan.Context()))
	if result := tx.Save(&OrderInfo); result.RowsAffected == 0 {
		//业务回滚
		tx.Rollback()

		o.Code = codes.Internal
		o.Detail = "创建订单失败"
		return primitive.CommitMessageState

	}

	querySaveSpan.Finish()

	o.OrderAmount = orderAmount
	o.ID = OrderInfo.ID

	//将订单id放入商品信息
	for _, orderGood := range orderGoods {
		orderGood.Order = OrderInfo.ID
	}

	queryBatchSpan := opentracing.GlobalTracer().StartSpan("query_batch", opentracing.ChildOf(parentSpan.Context()))

	//批量插入
	if result := tx.CreateInBatches(orderGoods, 100); result.RowsAffected == 0 {
		//业务回滚
		tx.Rollback()
		o.Code = codes.Internal
		o.Detail = "创建订单商品信息失败"
		return primitive.CommitMessageState
	}

	queryBatchSpan.Finish()

	queryDeleteSpan := opentracing.GlobalTracer().StartSpan("query_delete", opentracing.ChildOf(parentSpan.Context()))

	//清空购物车
	if result := tx.Where(&model.ShoppingCart{User: OrderInfo.User, Checked: true}).Delete(&model.ShoppingCart{}); result.RowsAffected == 0 {
		tx.Rollback()
		o.Code = codes.Internal
		o.Detail = "清空购物车失败"
		return primitive.CommitMessageState
	}

	queryDeleteSpan.Finish()

	//发送延时消息
	//超时回查
	//初始化生产者
	msg := primitive.NewMessage("order_timeout", msgs.Body)
	msg.WithDelayTimeLevel(4)
	_, err = global.MQOrder.SendSync(context.Background(), msg)
	if err != nil {
		zap.S().Errorf("发送延时消息失败: %v\n", err)
		tx.Rollback()
		o.Code = codes.Internal
		o.Detail = "发送延时消息失败"
		return primitive.CommitMessageState
	}

	//提交本地事务
	tx.Commit()
	o.Code = codes.OK

	fmt.Println("业务执行成功，不需要向mq提交消息")
	return primitive.RollbackMessageState
}

// CheckLocalTransaction When no response to prepare(half) message. broker will send check message to check the transaction status, and this method will be invoked to get local transaction status.
func (o *OrderListener) CheckLocalTransaction(msg *primitive.MessageExt) primitive.LocalTransactionState {
	//消息回查
	var OrderInfo model.OrderInfo
	_ = json.Unmarshal(msg.Body, &OrderInfo)

	//查订单号
	if result := global.DB.Where(model.OrderInfo{OrderSn: OrderInfo.OrderSn}).First(&OrderInfo); result.RowsAffected == 0 {
		return primitive.CommitMessageState //这里并不能说明库存被扣减了
	}

	return primitive.RollbackMessageState
}

// CreateOrder 新建订单
func (O *OrderServer) CreateOrder(ctx context.Context, req *proto.OrderRequest) (*proto.OrderInfoResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if req == nil || req.UserId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "用户信息无效")
	}
	// 交易运行时由统一的初始化流程提供，未就绪时不能开始写订单或扣库存。
	if global.ServerConfig == nil || global.DB == nil || global.GoodsSrvClient == nil ||
		global.InventorySrvClient == nil || global.MQOrder == nil || global.MQInventory == nil {
		return nil, status.Error(codes.FailedPrecondition, "订单交易服务尚未就绪")
	}
	//新建订单：
	//1. 从购物车获取商品信息
	//2. 去查询商品服务(跨服务)
	//3. 调用库存服务扣减库存(跨服务)
	//4.订单的基本信息表 和订单的商品信息表
	//5.从购物车中删除已购买记录

	//使用消息队列,将扣减库存消息放入mq中

	//初始化生产者
	orderListener := OrderListener{Ctx: ctx}
	q, err := rocketmq.NewTransactionProducer(&orderListener,
		producer.WithNameServer([]string{fmt.Sprintf("%s:%d", global.ServerConfig.MqInfo.Host, global.ServerConfig.MqInfo.Port)}))
	if err != nil {
		zap.S().Errorf("生成生产者失败：%s", err.Error())
		return nil, err
	}

	//启动生产者
	if err = q.Start(); err != nil {
		zap.S().Errorf("启动生产者失败：%s", err.Error())
		return nil, err
	}

	//生成订单的基本信息表
	order := model.OrderInfo{
		User:         req.UserId,
		OrderSn:      GenerationSn(req.UserId),
		Address:      req.Address,
		SignerName:   req.Name,
		SingerMobile: req.Mobile,
		Post:         req.Post,
	}

	jsonString, err := json.Marshal(&order)
	if err != nil {
		zap.S().Errorf("结构体转换失败:%s", err)
	}

	//发送消息
	msg := primitive.NewMessage("order-reback", jsonString)
	_, err = q.SendMessageInTransaction(context.Background(), msg)
	if err != nil {
		fmt.Printf("发送失败%s", err)
		return nil, status.Errorf(codes.Internal, "发送消息失败")
	}

	err = q.Shutdown()
	if err != nil {
		panic("shutdown fail err")
	}

	if orderListener.Code != codes.OK {
		return nil, status.Errorf(codes.Internal, orderListener.Detail)
	}

	return &proto.OrderInfoResponse{Id: orderListener.ID, OrderSn: order.OrderSn, Total: orderListener.OrderAmount}, nil
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
		Rsp.Data = append(Rsp.Data, &proto.OrderInfoResponse{
			Id:      order.ID,
			UserId:  order.User,
			OrderSn: order.OrderSn,
			PayType: order.PayType,
			Status:  order.Status,
			Post:    order.Post,
			Address: order.Address,
			Name:    order.SignerName,
			Mobile:  order.SingerMobile,
			Total:   order.OrderMount,
			AddTime: order.CreatedAt.Format("2006-04-02 15:04:05"),
		})
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

	OrderInfo := proto.OrderInfoResponse{}

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
	//先查询后更新，效率较低，直接更新查询
	if result := global.DB.Model(&model.OrderInfo{}).Where("order_sn = ?", req.OrderSn).Update("status", req.Status); result.RowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "订单不存在")
	}
	return &empty.Empty{}, nil
}

func TimeOutReback(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for i := range msgs {
		var OrderInfo model.OrderInfo
		_ = json.Unmarshal(msgs[i].Body, &OrderInfo)

		//如果支付成功，什么都不做，超时支付则归还库存
		var order model.OrderInfo
		if result := global.DB.Model(&model.OrderInfo{}).Where(&model.OrderInfo{OrderSn: OrderInfo.OrderSn}).First(&order); result.RowsAffected == 0 {
			return consumer.ConsumeSuccess, nil
		}

		fmt.Println("订单状态", order.Status)

		if order.Status != "TRADE_SUCCESS" {
			//归还库存，模仿order发送消息到order-reback中

			//事务
			tx := global.DB.Begin()

			//更改订单状态支付成功
			order.Status = "TRADE_CLOSED"
			tx.Save(&order)

			mq := primitive.NewMessage("order-reback", msgs[i].Body)

			_, err := global.MQInventory.SendSync(context.Background(), mq)
			if err != nil {
				tx.Rollback()
				zap.S().Errorf("发送失败%s", err)
				return consumer.ConsumeRetryLater, nil
			}

			tx.Commit()
			return consumer.ConsumeSuccess, nil

		}

	}
	return consumer.ConsumeSuccess, nil
}
