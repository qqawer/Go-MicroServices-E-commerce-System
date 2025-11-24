package handler

import("grpctest/goods_srv/model/proto"

)

type GoodsServer struct{
	proto.UnimplementedGoodsServer 
}
// // 商品接口
// 	func (s *GoodsServer) GoodsList(context.Context, *proto.GoodsFilterRequest) (*proto.GoodsListResponse, error){
// 		return nil, nil
// 	}
// 	// 现在用户提交订单有多个商品，你得批量查询商品的信息吧
// 	BatchGetGoods(context.Context, *BatchGoodsIdInfo) (*GoodsListResponse, error)
// 	CreateGoods(context.Context, *CreateGoodsInfo) (*GoodsInfoResponse, error)
// 	DeleteGoods(context.Context, *DeleteGoodsInfo) (*emptypb.Empty, error)
// 	UpdateGoods(context.Context, *CreateGoodsInfo) (*emptypb.Empty, error)
// 	GetGoodsDetail(context.Context, *GoodInfoRequest) (*GoodsInfoResponse, error)