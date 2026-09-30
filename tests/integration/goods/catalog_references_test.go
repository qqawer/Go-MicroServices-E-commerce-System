//go:build integration

package goods_test

import (
	"encoding/json"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
)

func TestBrandReferencedByGoods(t *testing.T) {
	ctx, c := categoryClient(t)
	_, deleted := c.DeleteBrand(ctx, &proto.BrandRequest{Id: 1})
	detail, err := c.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil {
		t.Fatal(err)
	}
	relations, err := c.CategoryBrandList(ctx, &proto.CategoryBrandFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("delete=%s goods=%d brand=%v relations=%v", status.Code(deleted), detail.Id, detail.Brand, relations.Data)
	if status.Code(deleted) != codes.FailedPrecondition || detail.GetBrand().GetId() != 1 {
		t.Fatal("referenced brand deletion must be rejected and goods association preserved")
	}
}

func TestCategoryReferencedByGoods(t *testing.T) {
	ctx, c := categoryClient(t)
	_, deleted := c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: 1})
	detail, err := c.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("delete=%s goods=%d category_id=%d category=%v", status.Code(deleted), detail.Id, detail.CategoryId, detail.Category)
	if status.Code(deleted) != codes.FailedPrecondition || detail.GetCategory().GetId() != 1 {
		t.Fatal("referenced category deletion must be rejected and goods association preserved")
	}
}

func TestCategoryWithChildren(t *testing.T) {
	ctx, c := categoryClient(t)
	parent, err := c.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Audit parent", Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Audit child", Level: 2, ParentCategory: parent.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, deleted := c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: parent.Id})
	detail, err := c.GetSubCategory(ctx, &proto.CategoryListRequest{Id: child.Id})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.GetAllCategorysList(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	var roots []model.Category
	if err := json.Unmarshal([]byte(tree.JsonData), &roots); err != nil {
		t.Fatal(err)
	}
	visible := false
	for _, root := range roots {
		if root.ID == parent.Id {
			visible = true
		}
	}
	t.Logf("delete=%s child=%d child_parent=%d parent_in_tree=%t", status.Code(deleted), detail.Info.Id, detail.Info.ParentCategory, visible)
	if status.Code(deleted) != codes.FailedPrecondition || !visible {
		t.Fatal("category with live children must remain in the category tree")
	}
}

func TestCatalogDeleteAfterReferencesRemoved(t *testing.T) {
	ctx, c := categoryClient(t)
	cat, err := c.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Delete fixture", Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	brand, err := c.CreateBrand(ctx, &proto.BrandRequest{Name: "Delete fixture"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Delete child", Level: 2, ParentCategory: cat.Id})
	if err != nil {
		t.Fatal(err)
	}
	relation, err := c.CreateCategoryBrand(ctx, &proto.CategoryBrandRequest{CategoryId: cat.Id, BrandId: brand.Id})
	if err != nil {
		t.Fatal(err)
	}
	goods, err := c.CreateGoods(ctx, &proto.CreateGoodsInfo{RequestKey: "delete-reference-fixture", Name: "Pending goods", GoodsSn: "DELETE-REF", CategoryId: cat.Id, BrandId: brand.Id})
	if err != nil {
		t.Fatal(err)
	}
	if goods.InventoryReady || goods.OnSale {
		t.Fatal("fixture must include pending/off-sale goods")
	}
	if _, err = c.DeleteBrand(ctx, &proto.BrandRequest{Id: brand.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err = c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: cat.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err = c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: child.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteGoods(ctx, &proto.DeleteGoodsInfo{Id: goods.Id}); err != nil {
		t.Fatal(err)
	}
	// The relation alone must still prevent deletion after goods/children are gone.
	if _, err = c.DeleteBrand(ctx, &proto.BrandRequest{Id: brand.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err = c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: cat.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err = c.DeleteCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: relation.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteBrand(ctx, &proto.BrandRequest{Id: brand.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: cat.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteBrand(ctx, &proto.BrandRequest{Id: brand.Id}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if _, err = c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: cat.Id}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	for _, id := range []int32{0, -1} {
		if _, err = c.DeleteBrand(ctx, &proto.BrandRequest{Id: id}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
		if _, err = c.DeleteCategory(ctx, &proto.DeleteCategoryRequest{Id: id}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
}
