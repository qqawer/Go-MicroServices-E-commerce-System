package main

import (
	"crypto/md5"
	"encoding/hex"

	"grpctest/inventory_srv/model"
	"io"
	"log"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func genMd5(code string) string {
	Md5 := md5.New()
	_,_=io.WriteString(Md5,code)
	return hex.EncodeToString(Md5.Sum(nil))
}

func main(){
	dsn := "root:123456@tcp(192.168.11.210:3306)/grpctest_inventory_srv?charset=utf8mb4&parseTime=True&loc=Local"
	
	newLogger:=logger.New(
		log.New(os.Stdout,"\r\n",log.LstdFlags),
		logger.Config{
			SlowThreshold:time.Second,//慢查询阈值
			LogLevel:logger.Info,
			Colorful:true,
		},
	)

	//全局模式
	db,err:=gorm.Open(mysql.Open(dsn),&gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
		Logger:newLogger,
	})
	if err!=nil{
		panic(err)
	}
	_=db.AutoMigrate(&model.Inventory{}, &model.StockSellDetail{})
    
}