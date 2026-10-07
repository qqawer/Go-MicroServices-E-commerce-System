// goods-es-demo-step performs one teaching step against the isolated catalog.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"grpctest/goods_srv/config"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/initialize"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	path := flag.String("config", "", "isolated demo JSON")
	step := flag.String("step", "", "create, ready, update, off, or delete")
	id := flag.Int("id", 0, "ID returned by create")
	key := flag.String("key", "ESFLOW20261007", "unique request key and fixture SKU")
	flag.Parse()
	switch *step {
	case "create", "ready", "update", "off", "delete":
	default:
		return fmt.Errorf("choose one supported step")
	}
	body, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var cfg config.ServerConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return err
	}
	if cfg.MysqlInfo.Host != "mysql" || cfg.MysqlInfo.Name != "es_store_demo" ||
		cfg.Elasticsearch.URL != "http://es:9200" || cfg.Elasticsearch.Index != "grpctest-goods-store-demo-ik" || !cfg.Elasticsearch.Enabled {
		return fmt.Errorf("this command requires the isolated demo database and IK index")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := grpc.NewClient("grpctest-es-store-goods-rpc:50052", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := proto.NewGoodsClient(conn)
	req := &proto.CreateGoodsInfo{
		Name: *key + "A 松露礼盒", GoodsSn: *key, GoodsBrief: "独立同步验收样例",
		CategoryId: 1, BrandId: 1, ShopPrice: 44.9, MarketPrice: 60,
		OnSale: true, RequestKey: *key,
	}
	if *step == "create" {
		result, err := client.CreateGoods(ctx, req)
		if err != nil {
			return err
		}
		fmt.Printf("created id=%d name=%s price=%.2f ready=%t on_sale=%t\n", result.Id, result.Name, result.ShopPrice, result.InventoryReady, result.OnSale)
		return nil
	}
	if *id <= 0 || int64(*id) > 2147483647 {
		return fmt.Errorf("supply the positive int32 ID returned by create")
	}
	global.ServerConfig = &cfg
	initialize.InitDB()
	db, err := global.DB.DB()
	if err != nil {
		return err
	}
	defer db.Close()
	var goods model.Goods
	if err := global.DB.WithContext(ctx).First(&goods, *id).Error; err != nil {
		return err
	}
	if goods.GoodsSn != *key {
		return fmt.Errorf("refusing to change a product outside this fixture")
	}
	switch *step {
	case "ready":
		// Fixture setup only: no real inventory service runs in this demo.
		if err := initialize.InitES(cfg.Elasticsearch); err != nil {
			return err
		}
		defer global.EsClient.Close(context.Background())
		err = global.DB.WithContext(ctx).Model(&model.Goods{BaseModel: model.BaseModel{ID: goods.ID}}).
			Updates(map[string]interface{}{"inventory_ready": true, "on_sale": true}).Error
	case "update":
		req.Id, req.RequestKey, req.Name, req.ShopPrice = goods.ID, "", "syncrenameprobe 松露礼盒", 39.9
		_, err = client.UpdateGoods(ctx, req)
	case "off":
		onSale := false
		_, err = client.UpdateGoodsStatus(ctx, &proto.UpdateGoodsStatusRequest{Id: goods.ID, OnSale: &onSale})
	case "delete":
		_, err = client.DeleteGoods(ctx, &proto.DeleteGoodsInfo{Id: goods.ID})
	}
	if err != nil {
		return err
	}
	fmt.Printf("completed step=%s id=%d\n", *step, goods.ID)
	return nil
}
