package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// ES paginates candidates; MySQL checks the current product state. Total is the
// index's hit count, so a page may shrink after this check. SearchIndex makes
// that contract explicit to the Web and storefront instead of claiming a SQL count.
func searchStoreGoods(ctx context.Context, req *proto.GoodsFilterRequest) (*proto.GoodsListResponse, error) {
	keyword := strings.TrimSpace(req.KeyWords)
	if req.Pages < 0 || req.PagePerNums < 0 || req.PriceMin < 0 || req.PriceMax < 0 || req.Brand < 0 || req.TopCategory < 0 ||
		utf8.RuneCountInString(keyword) > 100 || (req.PriceMax > 0 && req.PriceMin > req.PriceMax) {
		return nil, status.Error(codes.InvalidArgument, "商品查询参数无效")
	}
	page, size := max(int64(req.Pages), 1), int64(req.PagePerNums)
	if size == 0 {
		size = 10
	}
	size = min(size, 100)
	offset := (page - 1) * size
	if offset >= 10000 {
		return nil, status.Error(codes.InvalidArgument, "搜索最多浏览前 10000 条，请缩小筛选范围")
	}
	size = min(size, 10000-offset)
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}

	db := global.DB.WithContext(ctx).Model(&model.Goods{}).
		Where("on_sale = ? AND inventory_ready = ?", true, true)
	filters := []any{
		map[string]any{"term": map[string]any{"on_sale": true}},
		map[string]any{"term": map[string]any{"inventory_ready": true}},
	}
	for _, flag := range []struct {
		field string
		set   bool
	}{{"is_hot", req.IsHot}, {"is_new", req.IsNew}} {
		if flag.set {
			filters = append(filters, map[string]any{"term": map[string]any{flag.field: true}})
			db = db.Where(flag.field+" = ?", true)
		}
	}
	price := map[string]any{}
	if req.PriceMin > 0 {
		price["gte"] = int64(req.PriceMin) * 100
		db = db.Where("CAST(shop_price AS DECIMAL(18,2)) >= ?", req.PriceMin)
	}
	if req.PriceMax > 0 {
		price["lte"] = int64(req.PriceMax) * 100
		db = db.Where("CAST(shop_price AS DECIMAL(18,2)) <= ?", req.PriceMax)
	}
	if len(price) > 0 {
		filters = append(filters, map[string]any{"range": map[string]any{"price_cents": price}})
	}
	if req.Brand > 0 {
		filters = append(filters, map[string]any{"term": map[string]any{"brand_id": req.Brand}})
		db = db.Where("brands_id = ?", req.Brand)
	}
	if req.TopCategory > 0 {
		categoryDB := global.DB.WithContext(ctx)
		var category model.Category
		if err := categoryDB.First(&category, req.TopCategory).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, status.Error(codes.NotFound, "商品分类不存在")
			}
			return nil, storeGoodsListError(ctx, err)
		}
		categories := categoryDB.Model(&model.Category{}).Where("id = ?", category.ID)
		if category.Level < 3 {
			categories = categories.Or("parent_category_id = ?", category.ID)
		}
		if category.Level == 1 {
			children := categoryDB.Model(&model.Category{}).Select("id").Where("parent_category_id = ?", category.ID)
			categories = categories.Or("parent_category_id IN (?)", children)
		}
		var ids []int32
		if err := categories.Pluck("id", &ids).Error; err != nil {
			return nil, storeGoodsListError(ctx, err)
		}
		filters = append(filters, map[string]any{"terms": map[string]any{"category_id": ids}})
		db = db.Where("category_id IN ?", ids)
	}
	query := map[string]any{"filter": filters}
	if keyword != "" {
		query["must"] = map[string]any{"multi_match": map[string]any{
			"query": keyword, "fields": []string{"name^2", "brief"},
		}}
	}
	body, err := json.Marshal(map[string]any{
		"query": map[string]any{"bool": query}, "from": offset, "size": size,
		"track_total_hits": true, "_source": false,
		"sort": []any{map[string]string{"_score": "desc"}, map[string]string{"id": "asc"}},
	})
	if err != nil {
		return nil, searchGoodsError(ctx, err)
	}
	searchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(searchCtx, http.MethodPost,
		"/"+global.EsIndex+"/_search?allow_partial_search_results=false", bytes.NewReader(body))
	if err != nil {
		return nil, searchGoodsError(searchCtx, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := global.EsClient.Perform(httpReq)
	if err != nil {
		return nil, searchGoodsError(searchCtx, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, searchGoodsError(searchCtx, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, searchGoodsError(searchCtx, fmt.Errorf("ES search HTTP %d", res.StatusCode))
	}
	var result struct {
		TimedOut bool                 `json:"timed_out"`
		Shards   struct{ Failed int } `json:"_shards"`
		Hits     struct {
			Total struct {
				Value    int64
				Relation string
			}
			Hits []struct {
				ID string `json:"_id"`
			}
		}
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, searchGoodsError(searchCtx, err)
	}
	if result.TimedOut || result.Shards.Failed != 0 || result.Hits.Total.Relation != "eq" ||
		result.Hits.Total.Value < 0 || result.Hits.Total.Value > 2147483647 || int64(len(result.Hits.Hits)) > size ||
		(len(result.Hits.Hits) > 0 && offset+int64(len(result.Hits.Hits)) > result.Hits.Total.Value) {
		return nil, searchGoodsError(searchCtx, fmt.Errorf("incomplete or invalid ES search response"))
	}
	response := &proto.GoodsListResponse{Total: int32(result.Hits.Total.Value), SearchIndex: true, Data: make([]*proto.GoodsInfoResponse, 0)}
	ids := make([]int32, 0, len(result.Hits.Hits))
	seen := make(map[int32]bool)
	for _, hit := range result.Hits.Hits {
		id, err := strconv.ParseInt(hit.ID, 10, 32)
		if err != nil || id <= 0 || seen[int32(id)] {
			return nil, searchGoodsError(searchCtx, fmt.Errorf("invalid or repeated product ID in ES response"))
		}
		seen[int32(id)] = true
		ids = append(ids, int32(id))
	}
	if len(ids) == 0 {
		return response, nil // Never let an empty ID slice remove the SQL predicate.
	}
	var goods []model.Goods
	if err := db.Where("id IN ?", ids).Preload("Category").Preload("Brands").Find(&goods).Error; err != nil {
		return nil, storeGoodsListError(ctx, err)
	}
	byID := make(map[int32]model.Goods, len(goods))
	for _, good := range goods {
		byID[good.ID] = good
	}
	for _, id := range ids { // SQL IN does not preserve ES relevance order.
		if good, ok := byID[id]; ok {
			item := ModelToResponse(good)
			response.Data = append(response.Data, &item)
		}
	}
	return response, nil
}

func searchGoodsError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	zap.S().Errorw("ES 商品搜索失败", "error", err)
	return status.Error(codes.Unavailable, "商品搜索暂不可用")
}
