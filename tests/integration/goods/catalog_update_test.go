//go:build integration

package goods_test

import (
	"database/sql"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
)

func TestCategoryUpdatePersistsFalseAndPreservesHierarchy(t *testing.T) {
	ctx, client := categoryClient(t)
	var parent int32
	for level := int32(1); level <= 3; level++ {
		created, err := client.CreateCategory(ctx, &proto.CategoryInfoRequest{Name: "Original", Level: level, ParentCategory: parent, IsTab: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, enabled := range []bool{false, false, true} {
			if _, err := client.UpdateCategory(ctx, &proto.CategoryInfoRequest{Id: created.Id, Name: "Edited", IsTab: enabled}); err != nil {
				t.Fatal(err)
			}
			got, err := client.GetSubCategory(ctx, &proto.CategoryListRequest{Id: created.Id})
			if err != nil || got.GetInfo().GetName() != "Edited" || got.GetInfo().GetIsTab() != enabled ||
				got.GetInfo().GetLevel() != level || got.GetInfo().GetParentCategory() != parent {
				t.Fatalf("update must persist fields and preserve hierarchy: %v, %v", got, err)
			}
		}
		if level == 1 {
			var parentID sql.NullInt64
			if err := global.DB.Raw("SELECT parent_category_id FROM category WHERE id = ?", created.Id).Row().Scan(&parentID); err != nil || parentID.Valid {
				t.Fatalf("root parent must remain SQL NULL: %v, %v", parentID, err)
			}
		}
		parent = created.Id
	}
}

func TestBrandUpdateCanFillEmptyLogo(t *testing.T) {
	ctx, client := categoryClient(t)
	brand := model.Brands{Name: "Old brand"}
	if err := global.DB.Create(&brand).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.UpdateBrand(ctx, &proto.BrandRequest{Id: brand.ID, Name: "Edited brand", Logo: "https://example.invalid/new.png"}); err != nil {
			t.Fatal(err)
		}
		var got model.Brands
		if err := global.DB.First(&got, brand.ID).Error; err != nil || got.Name != "Edited brand" || got.Logo != "https://example.invalid/new.png" {
			t.Fatalf("brand name and initially empty logo must be persisted: %+v, %v", got, err)
		}
	}
}

func TestBannerUpdatePersistsFields(t *testing.T) {
	ctx, client := categoryClient(t)
	banner := model.Banner{Image: "https://example.invalid/old.png", Url: "https://example.invalid/old", Index: 3}
	if err := global.DB.Create(&banner).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.UpdateBanner(ctx, &proto.BannerRequest{Id: banner.ID, Image: "https://example.invalid/new.png", Url: "https://example.invalid/new", Index: 0}); err != nil {
			t.Fatal(err)
		}
		list, err := client.BannerList(ctx, &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, got := range list.Data {
			if got.Id == banner.ID {
				found = true
				if got.Image != "https://example.invalid/new.png" || got.Url != "https://example.invalid/new" || got.Index != 0 {
					t.Fatalf("banner changes must be persisted: %v", got)
				}
			}
		}
		if !found {
			t.Fatal("updated banner missing from list")
		}
	}
}
