//go:build integration

package goods_test

import (
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBrandNameConflictsPreserveExistingData(t *testing.T) {
	ctx, client := categoryClient(t)
	alpha, err := client.CreateBrand(ctx, &proto.BrandRequest{Name: "Alpha", Logo: "https://example.invalid/alpha.png"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := client.CreateBrand(ctx, &proto.BrandRequest{Name: "Beta", Logo: "https://example.invalid/beta.png"})
	if err != nil {
		t.Fatal(err)
	}
	checkConflict := func(err error) {
		t.Helper()
		if status.Code(err) != codes.AlreadyExists || status.Convert(err).Message() != "品牌名称已存在" {
			t.Errorf("expected name conflict, got %v", err)
		}
	}
	_, err = client.CreateBrand(ctx, &proto.BrandRequest{Name: "Alpha"})
	checkConflict(err)
	_, err = client.UpdateBrand(ctx, &proto.BrandRequest{Id: beta.Id, Name: "Alpha", Logo: "https://example.invalid/changed.png"})
	checkConflict(err)
	for _, expected := range []*proto.BrandInfoResponse{alpha, beta} {
		var got model.Brands
		if err := global.DB.First(&got, expected.Id).Error; err != nil || got.Name != expected.Name || got.Logo != expected.Logo {
			t.Fatalf("rejected operation changed existing brand: %+v, %v", got, err)
		}
	}
	// Keeping one's own name is allowed, including a repeated save.
	for i := 0; i < 2; i++ {
		if _, err := client.UpdateBrand(ctx, &proto.BrandRequest{Id: beta.Id, Name: "Beta", Logo: "https://example.invalid/updated.png"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.DeleteBrand(ctx, &proto.BrandRequest{Id: alpha.Id}); err != nil {
		t.Fatal(err)
	}
	// Soft deletion still occupies the existing database unique key.
	_, err = client.CreateBrand(ctx, &proto.BrandRequest{Name: "Alpha"})
	checkConflict(err)
	var count int64
	if err := global.DB.Unscoped().Model(&model.Brands{}).Where("name IN ?", []string{"Alpha", "Beta"}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("conflicting creation added or removed a brand: count=%d, %v", count, err)
	}
}
