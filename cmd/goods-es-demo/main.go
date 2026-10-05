// goods-es-demo runs the real goods RPC with isolated synthetic catalog data.
// It deliberately does not start inventory, orders, payment or cloud discovery.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"grpctest/goods_srv/config"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/initialize"
	"grpctest/goods_srv/model"
	"grpctest/goods_srv/model/proto"

	"google.golang.org/grpc"
	"gorm.io/gorm"
)

func main() {
	path := flag.String("config", "", "local demo service JSON")
	flag.Parse()
	body, err := os.ReadFile(*path)
	if err != nil {
		log.Fatal(err)
	}
	var cfg config.ServerConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		log.Fatal(err)
	}
	if cfg.MysqlInfo.Host != "mysql" || cfg.MysqlInfo.Name != "es_store_demo" ||
		cfg.Elasticsearch.URL != "http://es:9200" || cfg.Elasticsearch.Index != "grpctest-goods-store-demo" || !cfg.Elasticsearch.Enabled {
		log.Fatal("this runner requires the isolated es_store_demo database and ES lab target")
	}
	global.ServerConfig = &cfg
	initialize.InitDB()
	db, err := global.DB.DB()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := global.DB.AutoMigrate(&model.Category{}, &model.Brands{}, &model.Goods{}, &model.GoodsCreation{}); err != nil {
		log.Fatal(err)
	}
	if err := initialize.InitES(cfg.Elasticsearch); err != nil {
		log.Fatal(err)
	}
	defer global.EsClient.Close(context.Background())
	if err := seed(); err != nil {
		log.Fatal(err)
	}

	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatal(err)
	}
	server := grpc.NewServer()
	proto.RegisterGoodsServer(server, &handler.GoodsServer{})
	go func() {
		if err := server.Serve(lis); err != nil {
			log.Print(err)
		}
	}()
	log.Print("local goods RPC ready; Elasticsearch sync/search enabled; synthetic catalog only")
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	server.GracefulStop()
}

func seed() error {
	var count int64
	if err := global.DB.Model(&model.Goods{}).Unscoped().Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := global.DB.Transaction(func(tx *gorm.DB) error {
			for _, c := range []model.Category{
				{BaseModel: model.BaseModel{ID: 1}, Name: "生鲜水果", Level: 1},
				{BaseModel: model.BaseModel{ID: 2}, Name: "数码用品", Level: 1},
			} {
				if err := tx.Omit("ParentCategoryID").Create(&c).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&model.Brands{BaseModel: model.BaseModel{ID: 1}, Name: "学习演示品牌"}).Error; err != nil {
				return err
			}
			products := []struct {
				name, brief string
				price       float32
				category    int32
			}{
				{"烟台红富士苹果12个", "脆甜多汁，适合日常分享", 44.9, 1},
				{"青苹果手机 128G", "轻便拍照，清晰大屏", 5999, 2},
				{"山野果园礼盒", "红富士苹果搭配香梨，送给家人的水果组合", 39.9, 1},
				{"苹果手机保护壳", "轻薄防护，适配演示手机", 39.9, 2},
				{"鲜榨苹果汁", "清爽果香，冷藏更好喝", 12.5, 1},
				{"脐橙6个", "清甜多汁", 29.9, 1},
				{"库尔勒香梨", "清脆爽口", 24.9, 1},
				{"香蕉一把", "早餐搭配", 9.9, 1},
				{"蓝莓一盒", "酸甜小果", 19.9, 1},
				{"草莓一盒", "当季鲜果", 28.8, 1},
				{"猕猴桃6个", "软熟后风味更好", 22.5, 1},
				{"西瓜半颗", "夏日分享", 18, 1},
				{"水蜜桃4个", "香甜柔软", 26, 1},
				{"葡萄一串", "一口清甜", 21.5, 1},
				{"柠檬3个", "泡水与烹饪搭配", 8.8, 1},
				{"芒果3个", "浓郁果香", 32, 1},
			}
			for i, p := range products {
				g := model.Goods{Name: p.name, GoodsBrief: p.brief, ShopPrice: p.price, MarketPrice: p.price,
					GoodsSn: fmt.Sprintf("ES-DEMO-%d", i+1), CategoryID: p.category, BrandsID: 1,
					OnSale: true, InventoryReady: true, IsNew: i < 4, IsHot: i < 3, Images: model.GormList{}, DescImages: model.GormList{}}
				if err := tx.Omit("Category", "Brands").Create(&g).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	// Re-index existing demo rows on restart without changing their SQL values.
	var goods []model.Goods
	if err := global.DB.Find(&goods).Error; err != nil {
		return err
	}
	for _, g := range goods {
		if err := g.AfterUpdate(global.DB); err != nil {
			return err
		}
	}
	return nil
}
