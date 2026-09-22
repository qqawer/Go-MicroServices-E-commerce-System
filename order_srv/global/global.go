package global

import (
	// model "grpctest/model/user"
	"grpctest/order_srv/config"
	"grpctest/order_srv/proto"

	// "log"
	// "os"
	// "time"

	// "gorm.io/driver/mysql"
	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/go-redsync/redsync/v4"
	"gorm.io/gorm"
	// "gorm.io/gorm/logger"
	// "gorm.io/gorm/schema"
)

var (
	DB                 *gorm.DB
	ServerConfig       *config.ServerConfig
	NacosConfig        config.NacosConfig
	GoodsSrvClient     proto.GoodsClient
	InventorySrvClient proto.InventoryClient
	Rs                 *redsync.Redsync

	GroupInventory producer.Option
	GroupOrder     producer.Option
	MQOrder        rocketmq.Producer
	MQInventory    rocketmq.Producer
	MQTransactions rocketmq.TransactionProducer
)

// func init() {
// 	newLogger := logger.New(
// 		log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
// 		logger.Config{
// 			SlowThreshold:             time.Second, // Slow SQL threshold
// 			LogLevel:                  logger.Info, // Log level
// 			IgnoreRecordNotFoundError: true,        // Ignore ErrRecordNotFound error for logger
// 			ParameterizedQueries:      false,       // Don't include params in the SQL log
// 			Colorful:                  true,        // Disable color
// 		},
// 	)
// 	var err error
// 	// 参考 https://github.com/go-sql-driver/mysql#dsn-data-source-name 获取详情
// 	dsn := "root:123456@tcp(127.0.0.1:3306)/grpctest?charset=utf8mb4&parseTime=True&loc=Local"
// 	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
// 		NamingStrategy: schema.NamingStrategy{
// 			SingularTable: true,
// 		},
// 		//这样字表明就不会带s
// 		Logger: newLogger,
// 	})
// 	if err != nil {
// 		panic(err)
// 	}
// 	// DB.AutoMigrate(&model.User{})
// }

// 	//设置全局的logger,这个logger在我们执行每个sql语句的时候会打印每一行sql
// 	//sql才是最重要的，本着这个的原则我尽量的给大家看到每个api背后的sql语句是什么

// 	//定义一个表结构，将表结构直接生成对应的表 - migrations
// 	//迁移schema

//   // Migrate the schema
