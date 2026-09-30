//go:build integration

package goods_test

import (
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestBannerPriorityPersistsAndOrdersList(t *testing.T) {
	ctx, client := categoryClient(t)
	var ids []int32
	for _, index := range []int32{5, 0, 2, 0} {
		created, err := client.CreateBanner(ctx, &proto.BannerRequest{
			Image: "https://example.invalid/banner.png", Url: "https://example.invalid", Index: index,
		})
		if err != nil || created.GetId() <= 0 || created.GetIndex() != index {
			t.Fatalf("create must preserve index %d: %v, %v", index, created, err)
		}
		var saved model.Banner
		if err := global.DB.First(&saved, created.Id).Error; err != nil || saved.Index != index {
			t.Fatalf("stored index must match requested %d: %+v, %v", index, saved, err)
		}
		ids = append(ids, created.Id)
	}
	checkOrder := func(want []int32) {
		t.Helper()
		list, err := client.BannerList(ctx, &emptypb.Empty{})
		if err != nil || list.GetTotal() != int32(len(want)) || len(list.GetData()) != len(want) {
			t.Fatalf("list banners: %v, %v", list, err)
		}
		for i, id := range want {
			if list.Data[i].Id != id {
				t.Fatalf("expected banner IDs %v in priority/ID order, got %v", want, list.Data)
			}
		}
	}
	checkOrder([]int32{ids[1], ids[3], ids[2], ids[0]})
	if _, err := client.UpdateBanner(ctx, &proto.BannerRequest{Id: ids[0], Index: 0}); err != nil {
		t.Fatal(err)
	}
	checkOrder([]int32{ids[0], ids[1], ids[3], ids[2]})
}
