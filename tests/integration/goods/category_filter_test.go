//go:build integration

package goods_test

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm/clause"
)

func TestGoodsListCategorySubtree(t *testing.T) {
	ctx, client := categoryClient(t)
	category := func(level, parent int32) int32 {
		t.Helper()
		r, err := client.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Filter category", Level: level, ParentCategory: parent})
		if err != nil {
			t.Fatal(err)
		}
		return r.Id
	}
	root := category(1, 0)
	child := category(2, root)
	leaf := category(3, child)
	sibling := category(2, root)
	siblingLeaf := category(3, sibling)
	otherRoot := category(1, 0)
	empty := category(3, sibling)
	deletedLeaf := category(3, child)
	deletedBranch := category(2, root)
	belowDeleted := category(3, deletedBranch)
	brand := model.Brands{Name: "Filter other brand"}
	if err := global.DB.Create(&brand).Error; err != nil {
		t.Fatal(err)
	}
	goods := func(categoryID, brandID int32, price float32, sale, ready bool) int32 {
		t.Helper()
		var row model.Goods
		if err := global.DB.First(&row, 421).Error; err != nil {
			t.Fatal(err)
		}
		row.ID = 0
		row.Name = fmt.Sprintf("Category filter %d", categoryID)
		row.CategoryID, row.BrandsID, row.ShopPrice = categoryID, brandID, price
		row.OnSale, row.InventoryReady = sale, ready
		row.IsNew, row.IsHot = price == 20, price == 20
		if err := global.DB.Omit(clause.Associations).Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	a := goods(root, 1, 10, true, true)
	b := goods(child, 1, 20, true, true)
	c := goods(leaf, brand.ID, 30, true, true)
	d := goods(sibling, 1, 40, true, true)
	e := goods(siblingLeaf, brand.ID, 50, true, true)
	outside := goods(otherRoot, 1, 20, true, true)
	offSale := goods(root, 1, 20, false, true)
	pending := goods(child, 1, 20, true, false)
	deletedGoods := goods(root, 1, 20, true, true)
	goods(deletedLeaf, 1, 20, true, true)
	goods(deletedBranch, 1, 20, true, true)
	goods(belowDeleted, 1, 20, true, true)
	if err := global.DB.Delete(&model.Goods{}, deletedGoods).Error; err != nil {
		t.Fatal(err)
	}
	// Simulate legacy data: a deleted category and a live descendant below a
	// deleted branch must not be traversed by a live ancestor's filter.
	if err := global.DB.Delete(&model.Category{}, []int32{deletedLeaf, deletedBranch}).Error; err != nil {
		t.Fatal(err)
	}

	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprintf("store=%v", store), func(t *testing.T) {
			rootIDs, childIDs, hotIDs := []int32{a, b, c, d, e}, []int32{b, c}, []int32{b}
			if !store {
				rootIDs = append(rootIDs, offSale, pending)
				childIDs = append(childIDs, pending)
				hotIDs = append(hotIDs, offSale, pending)
			}
			for _, tc := range []struct {
				name     string
				category int32
				want     []int32
				filter   func(*proto.GoodsFilterRequest)
			}{
				{"root", root, rootIDs, nil},
				{"child", child, childIDs, nil},
				{"leaf", leaf, []int32{c}, nil},
				{"sibling", sibling, []int32{d, e}, nil},
				{"root without children", otherRoot, []int32{outside}, nil},
				{"empty category", empty, []int32{}, nil},
				{"brand", root, []int32{c, e}, func(r *proto.GoodsFilterRequest) { r.Brand = brand.ID }},
				{"price", root, []int32{c, d}, func(r *proto.GoodsFilterRequest) { r.PriceMin, r.PriceMax = 25, 45 }},
				{"new and hot", root, hotIDs, func(r *proto.GoodsFilterRequest) { r.IsNew, r.IsHot = true, true }},
				{"combined filters", root, []int32{c}, func(r *proto.GoodsFilterRequest) {
					r.Brand, r.PriceMin, r.PriceMax = brand.ID, 25, 35
					r.KeyWords = fmt.Sprintf("Category filter %d", leaf)
				}},
				{"no matching keyword", root, []int32{}, func(r *proto.GoodsFilterRequest) { r.KeyWords = "No such filter goods" }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := &proto.GoodsFilterRequest{TopCategory: tc.category, StoreVisibleOnly: store, KeyWords: "Category filter", Pages: 1, PagePerNums: 100}
					if tc.filter != nil {
						tc.filter(req)
					}
					list, err := client.GoodsList(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					ids := make([]int32, 0, len(list.Data))
					for _, row := range list.Data {
						ids = append(ids, row.Id)
						if row.Category.GetId() != row.CategoryId || row.Brand.GetId() <= 0 || (store && (!row.OnSale || !row.InventoryReady)) {
							t.Fatalf("lost associations or exposed hidden goods: %v", row)
						}
					}
					sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
					sort.Slice(tc.want, func(i, j int) bool { return tc.want[i] < tc.want[j] })
					if list.Total != int32(len(tc.want)) || !reflect.DeepEqual(ids, tc.want) {
						t.Fatalf("total=%d ids=%v; want total=%d ids=%v", list.Total, ids, len(tc.want), tc.want)
					}
					// Count must use the same subtree before applying the page limit.
					req.PagePerNums = 1
					page, err := client.GoodsList(ctx, req)
					wantRows := 0
					if len(tc.want) > 0 {
						wantRows = 1
					}
					if err != nil || page.GetTotal() != list.Total || len(page.GetData()) != wantRows {
						t.Fatalf("first page: %v, %v", page, err)
					}
					req.Pages = int32(len(tc.want) + 1)
					page, err = client.GoodsList(ctx, req)
					if err != nil || page.GetTotal() != list.Total || len(page.GetData()) != 0 {
						t.Fatalf("out-of-range page: %v, %v", page, err)
					}
				})
			}
			for _, id := range []int32{deletedLeaf, deletedBranch, 2147483647} {
				if _, err := client.GoodsList(ctx, &proto.GoodsFilterRequest{TopCategory: id, StoreVisibleOnly: store}); status.Code(err) != codes.NotFound {
					t.Fatalf("missing/deleted selected category %d: %v", id, err)
				}
			}
		})
	}
}
