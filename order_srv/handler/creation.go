package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"grpctest/order_srv/global"
	"grpctest/order_srv/model"
	"grpctest/order_srv/proto"
)

var requestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var mobilePattern = regexp.MustCompile(`^1[0-9]{10}$`)

type creationSnapshot struct {
	Order model.OrderInfo
	Cart  []model.ShoppingCart
	Goods []model.OrderGoods
	Stock []*proto.GoodsInventoryInfo
}

// This shared listener carries no request fields. Each execution owns its local
// result; RPC responses and broker checks read the durable transaction decision.
type TransactionListener struct{}

func (*TransactionListener) ExecuteLocalTransaction(msg *primitive.Message) primitive.LocalTransactionState {
	return (&OrderListener{}).ExecuteLocalTransaction(msg)
}
func (*TransactionListener) CheckLocalTransaction(msg *primitive.MessageExt) primitive.LocalTransactionState {
	return (&OrderListener{}).CheckLocalTransaction(msg)
}

func validateCreation(req *proto.OrderRequest) error {
	if req.GetUserId() <= 0 || !requestKeyPattern.MatchString(req.GetRequestKey()) {
		return status.Error(codes.InvalidArgument, "用户及 Idempotency-Key 无效")
	}
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 20 || req.Address == "" || utf8.RuneCountInString(req.Address) > 100 || !mobilePattern.MatchString(req.Mobile) || utf8.RuneCountInString(req.Post) > 20 {
		return status.Error(codes.InvalidArgument, "收货人、地址、手机号或留言无效")
	}
	return nil
}

func requestHash(req *proto.OrderRequest) string {
	body, _ := json.Marshal([]string{strconv.Itoa(int(req.UserId)), req.Name, req.Address, req.Mobile, req.Post})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func loadCreation(ctx context.Context, user int32, key string) (model.OrderTransaction, error) {
	var record model.OrderTransaction
	err := global.DB.WithContext(ctx).Where("user_id = ? AND request_key = ?", user, key).First(&record).Error
	return record, err
}

func prepareCreation(ctx context.Context, req *proto.OrderRequest) (model.OrderTransaction, error) {
	record, err := loadCreation(ctx, req.UserId, req.RequestKey)
	hash := requestHash(req)
	if err == nil {
		if record.RequestHash != hash {
			return record, status.Error(codes.AlreadyExists, "此请求编号已用于不同的下单内容")
		}
		return record, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return record, err
	}
	var cart []model.ShoppingCart
	if err := global.DB.WithContext(ctx).Where("user = ? AND checked = ?", req.UserId, true).Order("id").Limit(1001).Find(&cart).Error; err != nil {
		return record, err
	}
	if len(cart) == 0 || len(cart) > 1000 {
		return record, status.Error(codes.InvalidArgument, "请勾选 1 至 1000 条购物车记录")
	}
	ids := make([]int32, 0, len(cart))
	nums := map[int32]int32{}
	for _, item := range cart {
		if item.Goods <= 0 || item.Nums <= 0 || int64(nums[item.Goods])+int64(item.Nums) > math.MaxInt32 {
			return record, status.Error(codes.InvalidArgument, "购物车数量无效")
		}
		if nums[item.Goods] == 0 {
			ids = append(ids, item.Goods)
		}
		nums[item.Goods] += item.Nums
	}
	goods, err := global.GoodsSrvClient.BatchGetGoods(ctx, &proto.BatchGoodsIdInfo{Id: ids})
	if err != nil {
		return record, err
	}
	if goods == nil || len(goods.Data) != len(ids) {
		return record, status.Error(codes.FailedPrecondition, "购物车中存在无效商品")
	}
	snapshot := creationSnapshot{Cart: cart, Order: model.OrderInfo{User: req.UserId, OrderSn: GenerationSn(req.UserId), Address: req.Address, SignerName: req.Name, SingerMobile: req.Mobile, Post: req.Post, PayType: "demo"}}
	var total int64
	seen := map[int32]bool{}
	for _, item := range goods.Data {
		if item == nil || nums[item.Id] <= 0 || seen[item.Id] {
			return record, status.Error(codes.Internal, "商品清单不一致")
		}
		if !item.OnSale || !item.InventoryReady {
			return record, status.Error(codes.FailedPrecondition, "存在未上架或库存未初始化的商品")
		}
		seen[item.Id] = true
		// The legacy goods protocol stores float32 prices. Normalize its shortest
		// decimal representation once at checkout, then calculate only in cents.
		price, err := strconv.ParseFloat(strconv.FormatFloat(float64(item.ShopPrice), 'f', -1, 32), 64)
		if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 || price > 1e9 {
			return record, status.Error(codes.FailedPrecondition, "商品价格无效")
		}
		cents := int64(math.Round(price * 100))
		if cents <= 0 || math.Abs(price*100-float64(cents)) > 0.00001 {
			return record, status.Error(codes.FailedPrecondition, "商品价格不能超过两位小数")
		}
		if cents > (1e12-total)/int64(nums[item.Id]) {
			return record, status.Error(codes.InvalidArgument, "订单总金额超出范围")
		}
		total += cents * int64(nums[item.Id])
		snapshot.Goods = append(snapshot.Goods, model.OrderGoods{Goods: item.Id, GoodsName: item.Name, GoodsImage: item.GoodsFrontImage, GoodsPrice: float32(cents) / 100, UnitCents: &cents, Nums: nums[item.Id]})
		snapshot.Stock = append(snapshot.Stock, &proto.GoodsInventoryInfo{GoodsId: item.Id, Num: nums[item.Id]})
	}
	snapshot.Order.TotalCents = &total
	snapshot.Order.OrderMount = float32(total) / 100
	body, err := json.Marshal(snapshot)
	if err != nil {
		return record, err
	}
	raw := string(body)
	expiry := time.Now().Add(time.Duration(global.ServerConfig.MqInfo.OrderTTLSeconds) * time.Second)
	record = model.OrderTransaction{OrderSn: snapshot.Order.OrderSn, UserID: req.UserId, RequestKey: &req.RequestKey, RequestHash: hash, Snapshot: &raw, ExpiresAt: &expiry}
	// Race winner fixes the entire snapshot. A conflicting request never replaces it.
	err = global.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
	if err != nil {
		return record, err
	}
	record, err = loadCreation(ctx, req.UserId, req.RequestKey)
	if err == nil && record.RequestHash != hash {
		return record, status.Error(codes.AlreadyExists, "此请求编号已用于不同的下单内容")
	}
	return record, err
}

func creationResult(ctx context.Context, record model.OrderTransaction) (*proto.OrderInfoResponse, error) {
	if record.State == model.OrderPreparing {
		return &proto.OrderInfoResponse{OrderSn: record.OrderSn, Status: "CREATING", PayType: "demo"}, nil
	}
	if record.State == model.OrderCancelled {
		return &proto.OrderInfoResponse{OrderSn: record.OrderSn, Status: "CREATE_CANCELLED", PayType: "demo"}, nil
	}
	var order model.OrderInfo
	if err := global.DB.WithContext(ctx).Where("order_sn = ?", record.OrderSn).First(&order).Error; err != nil {
		return nil, err
	}
	return orderResponse(order), nil
}
func orderResponse(order model.OrderInfo) *proto.OrderInfoResponse {
	result := &proto.OrderInfoResponse{Id: order.ID, UserId: order.User, OrderSn: order.OrderSn, Status: order.Status, PayType: order.PayType, Total: order.OrderMount, Name: order.SignerName, Address: order.Address, Mobile: order.SingerMobile, Post: order.Post, AddTime: order.CreatedAt.Format(time.RFC3339)}
	if order.TotalCents != nil {
		result.TotalCents = *order.TotalCents
	}
	return result
}

func (o *OrderServer) CreateOrder(ctx context.Context, req *proto.OrderRequest) (*proto.OrderInfoResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if global.ServerConfig == nil || global.DB == nil || global.GoodsSrvClient == nil || global.InventorySrvClient == nil || global.MQTransactions == nil || global.MQOrder == nil || global.MQInventory == nil {
		return nil, status.Error(codes.FailedPrecondition, "订单交易服务尚未就绪")
	}
	if err := validateCreation(req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	record, err := prepareCreation(ctx, req)
	if err != nil {
		// Another same-key request may have committed and removed the cart while this
		// caller was building its candidate snapshot. Recover the winning request.
		winner, lookupErr := loadCreation(ctx, req.UserId, req.RequestKey)
		if lookupErr != nil {
			return nil, creationError(ctx, err)
		}
		if winner.RequestHash != requestHash(req) {
			return nil, status.Error(codes.AlreadyExists, "此请求编号已用于不同的下单内容")
		}
		record = winner
	}
	if record.State == model.OrderPreparing {
		body, _ := json.Marshal(struct {
			OrderSn string
			User    int32
		}{record.OrderSn, record.UserID})
		msg := primitive.NewMessage("order-reback", body)
		msg.WithKeys([]string{record.OrderSn})
		// A lost send response cannot prove the half message or local transaction failed.
		// Read the decision; pending intents are fenced and compensated by recovery.
		_, _ = global.MQTransactions.SendMessageInTransaction(ctx, msg)
		record, err = loadCreation(ctx, req.UserId, req.RequestKey)
		if err != nil {
			return nil, creationError(ctx, err)
		}
	}
	result, err := creationResult(ctx, record)
	return result, creationError(ctx, err)
}
func (o *OrderServer) GetOrderCreation(ctx context.Context, req *proto.OrderRequest) (*proto.OrderInfoResponse, error) {
	if req.GetUserId() <= 0 || !requestKeyPattern.MatchString(req.GetRequestKey()) {
		return nil, status.Error(codes.InvalidArgument, "用户及请求编号无效")
	}
	record, err := loadCreation(ctx, req.UserId, req.RequestKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "未找到此下单请求")
	}
	if err != nil {
		return nil, creationError(ctx, err)
	}
	result, err := creationResult(ctx, record)
	return result, creationError(ctx, err)
}
func creationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Unavailable, "下单结果暂时无法确认，请使用原 Idempotency-Key 查询或重试")
}

// Round the broker wakeup upwards. The persisted deadline remains authoritative
// and recovery handles long delays or missed deliveries without extending it.
func timeoutDelay(expiry *time.Time) int {
	if expiry == nil {
		return 4
	}
	seconds := time.Until(*expiry).Seconds()
	for i, d := range []int{1, 5, 10, 30, 60, 120, 180, 240, 300, 360, 420, 480, 540, 600, 1200, 1800, 3600, 7200} {
		if float64(d) >= seconds {
			return i + 1
		}
	}
	return 18
}
