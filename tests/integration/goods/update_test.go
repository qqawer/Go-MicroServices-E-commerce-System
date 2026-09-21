//go:build integration

package goods_test

import (
	"context"
	"strings"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"
	"grpctest/tests/integration/testdb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
)

func updateClient(t *testing.T) (context.Context, proto.GoodsClient) {
	t.Helper()
	previous := global.DB
	db := testdb.Open(t, "grpctest_goods_srv").Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	global.DB = db
	t.Cleanup(func() { db.Rollback(); global.DB = previous })
	if err := db.Model(&model.Goods{}).Where("id = ?", 421).Updates(map[string]interface{}{
		"on_sale": true, "is_new": true, "is_hot": true, "ship_free": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, conn := testdb.Client(t, func(server *grpc.Server) {
		proto.RegisterGoodsServer(server, &handler.GoodsServer{})
	})
	return ctx, proto.NewGoodsClient(conn)
}

func editedProfile() *proto.CreateGoodsInfo {
	return &proto.CreateGoodsInfo{
		Id: 421, Name: "Edited goods", GoodsSn: "TEST-421", CategoryId: 1, BrandId: 1,
		MarketPrice: 19.5, ShopPrice: 13.5, GoodsBrief: "Updated fixture", ShipFree: false,
		Images: []string{"https://example.invalid/edited.png"}, DescImages: []string{},
		GoodsFrontImage: "https://example.invalid/edited.png",
	}
}

func TestEditGoodsPreservesSaleStates(t *testing.T) {
	ctx, client := updateClient(t)
	if _, err := client.UpdateGoods(ctx, editedProfile()); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Edited goods" || got.ShopPrice != 13.5 || got.ShipFree || len(got.DescImages) != 0 {
		t.Fatal("profile changes were not saved")
	}
	if !got.OnSale || !got.IsNew || !got.IsHot {
		t.Fatalf("editing profile cleared states: sale=%v new=%v hot=%v", got.OnSale, got.IsNew, got.IsHot)
	}
}

func TestUpdateSingleGoodsState(t *testing.T) {
	ctx, client := updateClient(t)
	if _, err := client.UpdateGoodsStatus(ctx, &proto.UpdateGoodsStatusRequest{Id: 421, IsHot: gproto.Bool(false)}); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil {
		t.Fatal(err)
	}
	if got.IsHot || !got.IsNew || !got.OnSale || got.Name != "Integration goods" || got.ShopPrice != 12.5 || !got.ShipFree {
		t.Fatal("setting hot=false must preserve omitted states and product details")
	}
	// A subsequent profile edit must preserve the mixed state, not restore a snapshot.
	if _, err := client.UpdateGoods(ctx, editedProfile()); err != nil {
		t.Fatal(err)
	}
	got, err = client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil || got.IsHot || !got.IsNew || !got.OnSale {
		t.Fatal("profile edit changed independently updated states")
	}
}

func TestGoodsStatesCanBeDisabledAndEnabled(t *testing.T) {
	ctx, client := updateClient(t)
	for _, enabled := range []bool{false, false, true} {
		_, err := client.UpdateGoodsStatus(ctx, &proto.UpdateGoodsStatusRequest{
			Id: 421, IsNew: gproto.Bool(enabled), IsHot: gproto.Bool(enabled), OnSale: gproto.Bool(enabled),
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
		if err != nil || got.IsNew != enabled || got.IsHot != enabled || got.OnSale != enabled {
			t.Fatalf("could not persist all states as %v (including repeated requests)", enabled)
		}
	}
}

func TestGoodsUpdateRejectsInvalidTargets(t *testing.T) {
	ctx, client := updateClient(t)
	for _, test := range []struct {
		name                string
		id, category, brand int32
		want                codes.Code
	}{
		{"invalid goods", 0, 1, 1, codes.InvalidArgument},
		{"negative goods", -1, 1, 1, codes.InvalidArgument},
		{"missing goods", 2147483647, 1, 1, codes.NotFound},
		{"invalid category", 421, 0, 1, codes.InvalidArgument},
		{"missing category", 421, 2147483647, 1, codes.InvalidArgument},
		{"invalid brand", 421, 1, -1, codes.InvalidArgument},
		{"missing brand", 421, 1, 2147483647, codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := editedProfile()
			req.Id, req.CategoryId, req.BrandId = test.id, test.category, test.brand
			_, err := client.UpdateGoods(ctx, req)
			if status.Code(err) != test.want {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
	for _, test := range []struct {
		req  *proto.UpdateGoodsStatusRequest
		want codes.Code
	}{
		{&proto.UpdateGoodsStatusRequest{Id: 421}, codes.InvalidArgument},
		{&proto.UpdateGoodsStatusRequest{Id: 0, IsHot: gproto.Bool(false)}, codes.InvalidArgument},
		{&proto.UpdateGoodsStatusRequest{Id: 2147483647, IsHot: gproto.Bool(false)}, codes.NotFound},
	} {
		_, err := client.UpdateGoodsStatus(ctx, test.req)
		if status.Code(err) != test.want {
			t.Fatalf("got %v, want %v", err, test.want)
		}
	}
	got, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil || got.Name != "Integration goods" || !got.OnSale || !got.IsHot || !got.IsNew {
		t.Fatal("rejected requests changed the original product")
	}
}

func TestGoodsUpdateDatabaseFailureDoesNotReportSuccess(t *testing.T) {
	ctx, client := updateClient(t)
	req := editedProfile()
	req.GoodsBrief = strings.Repeat("x", 501) // Exceeds the real column limit.
	_, err := client.UpdateGoods(ctx, req)
	if status.Code(err) != codes.Internal {
		t.Fatalf("database rejected the update but RPC returned %v", err)
	}
	got, err := client.GetGoodsDetail(ctx, &proto.GoodInfoRequest{Id: 421})
	if err != nil || got.Name != "Integration goods" || got.ShopPrice != 12.5 || !got.OnSale {
		t.Fatal("failed update must preserve the original product")
	}
}
