package initialize

import (
	"fmt"
	"grpctest/user_srv/global"
	// model "grpctest/model/user"                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     

	"log"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	// "SimpleWeb/global"
)
func InitDB(){
	mysqlInfo:=global.ServerConfig.MysqlInfo

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
	mysqlInfo.User,
	mysqlInfo.Password,
	mysqlInfo.Host,
	mysqlInfo.Port,
	mysqlInfo.Name)
	newLogger:=logger.New(
		log.New(os.Stdout,"\r\n",log.LstdFlags),
		logger.Config{
			SlowThreshold:time.Second,//慢查询阈值
			LogLevel:logger.Info,
			Colorful:true,
		},
	)
	//全局模式
	var err error
	global.DB,err=gorm.Open(mysql.Open(dsn),&gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,//SingularTable: true：表示使用单数形式的表名。例如，定义的模型名为 User，创建的表名就是 user 而非默认的 users。
		},
		Logger:newLogger,
	})
	if err!=nil{
		panic(err)
	}
	// _=global.DB.AutoMigrate(&model.User{})



	// db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	// if(err!=nil){
	// 	log.Fatal("Failed to initialize database, got error :%v",err)
	// }

	// sqlDB,err:=db.DB()

	// // SetMaxIdleConns 用于设置连接池中空闲连接的最大数量。
	// sqlDB.SetMaxIdleConns(AppConfig.Database.MaxIdleConns)

	// // SetMaxOpenConns 设置打开数据库连接的最大数量。
	// sqlDB.SetMaxOpenConns(AppConfig.Database.MaxOpenConns)	

	// // SetConnMaxLifetime 设置了连接可复用的最大时间。
	// sqlDB.SetConnMaxLifetime(time.Hour)

	// global.Db=db
}

// 当设置 LogLevel: logger.Info 时，只有满足以下条件的日志会被打印到控制台：

// 慢查询：执行时间超过 SlowThreshold（这里设置为 1 秒）的 SQL 查询语句会被记录。
// 错误信息：执行 SQL 查询时出现错误，相关错误信息会被记录。
// 因此，普通的查询命令（执行时间小于 1 秒且没有出错）不会被打印到控制台。