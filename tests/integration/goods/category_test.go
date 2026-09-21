//go:build integration

package goods_test

import (
	"context"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func categoryClient(t *testing.T) (context.Context, proto.GoodsClient) {
	t.Helper()
	previous := global.DB
	db := testdb.Open(t, "grpctest_goods_srv").Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	global.DB = db
	t.Cleanup(func() { db.Rollback(); global.DB = previous })
	ctx, conn := testdb.Client(t, func(server *grpc.Server) { proto.RegisterGoodsServer(server, &handler.GoodsServer{}) })
	return ctx, proto.NewGoodsClient(conn)
}

func TestCategoryBrandListIDsLocateTheCorrectRelation(t *testing.T) {
	ctx, client := categoryClient(t)
	for _, brand := range []model.Brands{
		{BaseModel: model.BaseModel{ID: 2}, Name: "Second brand"},
		{BaseModel: model.BaseModel{ID: 3}, Name: "Third brand"},
		{BaseModel: model.BaseModel{ID: 4}, Name: "Fourth brand"},
	} {
		if err := global.DB.Create(&brand).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []model.GoodsCategoryBrand{
		{BaseModel: model.BaseModel{ID: 501}, CategoryID: 1, BrandsID: 2},
		{BaseModel: model.BaseModel{ID: 502}, CategoryID: 1, BrandsID: 3},
	} {
		if err := global.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	list, err := client.CategoryBrandList(ctx, &proto.CategoryBrandFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil || list.GetTotal() != 3 || len(list.GetData()) != 3 {
		t.Fatalf("list relations: %v, %v", list, err)
	}
	var selected int32
	for _, row := range list.Data {
		if row.GetBrand().GetId() == 2 {
			selected = row.Id
		}
	}
	if selected != 501 {
		t.Fatalf("list must return relation ID 501, not category ID 1; got %d", selected)
	}
	for i := 0; i < 2; i++ { // Repeating an unchanged update must still succeed.
		if _, err := client.UpdateCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: selected, CategoryId: 1, BrandId: 4}); err != nil {
			t.Fatal(err)
		}
	}
	list, err = client.CategoryBrandList(ctx, &proto.CategoryBrandFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil {
		t.Fatal(err)
	}
	brands := map[int32]int32{}
	for _, row := range list.Data {
		brands[row.Id] = row.GetBrand().GetId()
	}
	if brands[501] != 4 || brands[502] != 3 || brands[1] != 1 {
		t.Fatalf("update must affect only the selected relation: %v", brands)
	}
	if _, err := client.DeleteCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: selected}); err != nil {
		t.Fatal(err)
	}
	list, err = client.CategoryBrandList(ctx, &proto.CategoryBrandFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil || list.GetTotal() != 2 || len(list.GetData()) != 2 {
		t.Fatalf("delete must remove one relation: %v, %v", list, err)
	}
	for _, row := range list.Data {
		if row.Id != 1 && row.Id != 502 {
			t.Fatalf("delete affected the wrong relation: %v", row)
		}
	}
}

func TestCreatedCategoryCanBeReadByReturnedID(t *testing.T) {
	ctx, client := categoryClient(t)
	var parent int32
	for level := int32(1); level <= 3; level++ {
		created, err := client.CreateCategory(ctx, &proto.CategoryInfoRequest{
			Name: "New category", Level: level, ParentCategory: parent, IsTab: true,
		})
		if err != nil || created.GetId() <= 1 || created.GetId() == parent {
			t.Fatalf("create level %d must return a new persisted ID, got %v, %v", level, created, err)
		}
		if created.Name != "New category" || created.Level != level || created.ParentCategory != parent || !created.IsTab {
			t.Errorf("create response must contain the saved category fields: %v", created)
		}
		detail, err := client.GetSubCategory(ctx, &proto.CategoryListRequest{Id: created.Id})
		if err != nil || detail.GetInfo().GetName() != "New category" || detail.GetInfo().GetLevel() != level ||
			detail.GetInfo().GetParentCategory() != parent || !detail.GetInfo().GetIsTab() {
			t.Fatalf("returned ID must locate the created category: %v, %v", detail, err)
		}
		parent = created.Id
	}
}

func TestCategoryBrandDuplicatesAreRejectedWithoutChangingRelations(t *testing.T) {
	ctx, client := categoryClient(t)
	category, err := client.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Other category", Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateCategoryBrand(ctx, &proto.CategoryBrandRequest{CategoryId: category.Id, BrandId: 1})
	if err != nil || created.GetId() <= 1 {
		t.Fatalf("create relation must return persisted ID: %v, %v", created, err)
	}
	if got, err := client.CreateCategoryBrand(ctx, &proto.CategoryBrandRequest{CategoryId: category.Id, BrandId: 1}); got != nil || status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate create must return AlreadyExists, got %v, %v", got, err)
	}
	if _, err := client.UpdateCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: created.Id, CategoryId: 1, BrandId: 1}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("conflicting update must return AlreadyExists, got %v", err)
	}
	list, err := client.CategoryBrandList(ctx, &proto.CategoryBrandFilterRequest{Pages: 1, PagePerNums: 10})
	if err != nil || list.GetTotal() != 2 || len(list.GetData()) != 2 {
		t.Fatalf("rejected writes changed relations: %v, %v", list, err)
	}
	for _, row := range list.Data {
		if row.Id == created.Id && (row.GetCategory().GetId() != category.Id || row.GetBrand().GetId() != 1) {
			t.Fatalf("failed update changed original relation: %v", row)
		}
	}
}

func TestCreateCategoryRejectsInvalidHierarchy(t *testing.T) {
	ctx, client := categoryClient(t)
	for _, req := range []*proto.CategoryInfoRequest{
		{Name: " ", Level: 1},
		{Name: "Invalid category", Level: 0},
		{Name: "Invalid category", Level: 4},
		{Name: "Invalid category", Level: 1, ParentCategory: 1},
		{Name: "Invalid category", Level: 2},
		{Name: "Invalid category", Level: 2, ParentCategory: 2147483647},
		{Name: "Invalid category", Level: 3, ParentCategory: 1},
	} {
		if got, err := client.CreateCategory(ctx, req); got != nil || status.Code(err) != codes.InvalidArgument {
			t.Errorf("invalid category hierarchy must be rejected: request=%v result=%v error=%v", req, got, err)
		}
	}
}

func TestCategoryBrandRejectsInvalidOrMissingTargets(t *testing.T) {
	ctx, client := categoryClient(t)
	for _, req := range []*proto.CategoryBrandRequest{
		{CategoryId: 0, BrandId: 1}, {CategoryId: 1, BrandId: -1},
		{CategoryId: 2147483647, BrandId: 1}, {CategoryId: 1, BrandId: 2147483647},
	} {
		if _, err := client.CreateCategoryBrand(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("invalid relation must be rejected: request=%v error=%v", req, err)
		}
		req.Id = 1
		if _, err := client.UpdateCategoryBrand(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("invalid update must be rejected: request=%v error=%v", req, err)
		}
	}
	for _, id := range []int32{0, -1, 2147483647} {
		want := codes.InvalidArgument
		if id > 0 {
			want = codes.NotFound
		}
		if _, err := client.UpdateCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: id, CategoryId: 1, BrandId: 1}); status.Code(err) != want {
			t.Errorf("update invalid/missing ID: got %v, want %v", err, want)
		}
		if _, err := client.DeleteCategoryBrand(ctx, &proto.CategoryBrandRequest{Id: id}); status.Code(err) != want {
			t.Errorf("delete invalid/missing ID: got %v, want %v", err, want)
		}
	}
}
