package initialize

import (
	"fmt"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type Config struct {
	App     App     `mapstructure:"app"`
	Log     Log     `mapstructure:"log"`
	UserSrv UserSrv `mapstructure:"user_srv"`
	Redis   Redis   `mapstructure:"redis"`
	AliSms  AliSms  `mapstructure:"ali_sms"`
}
type App struct {
	Name string `mapstructure:"name"`
	Mode string `mapstructure:"mode"`
}
type Log struct {
	Level      string `mapstructure:"level"`
	Filename   string `mapstructure:"filename"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxAge     int    `mapstructure:"max_age"`
	MaxBackups int    `mapstructure:"max_backups"`
}
type UserSrv struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}
type Redis struct {
	Host     string `mapstructure:"host"`
	Password string `mapstructure:"password"`
	Port     int    `mapstructure:"port"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}
type AliSms struct {
	ApiKey    string `mapstructure:"key"`
	ApiSecret string `mapstructure:"secret"`
	Expire    int    `mapstructure:"expire"`
}

var AppConfig *Config

//	func GetEnvInfo(env string)bool{
//		viper.AutomaticEnv()
//		return viper.GetBool(env)
//	}
func Init() (err error) {
	// debug:=GetEnvInfo("MXSHOP_DEBUG")
	// configFilePrefix:="config"
	// configFileName:=fmt.Sprintf("viper_test/%s-pro.yaml",configFilePrefix)
	// if debug{
	// 	configFileName=fmt.Sprintf("viper_test/%s-debug.yaml",configFileName)
	// }
	// v:=viper.New()
	// v.SetConfigFile(configFileName)
	viper.SetConfigFile("./config/config.yaml")
	if err := viper.ReadInConfig(); err != nil {
		// zap.L().Fatal("Error reading config file: %v", zap.Error(err))
		fmt.Printf("viper.ReadInConfig() failed,err: %v\n", err)
		return err
	}
	AppConfig = &Config{}
	if err := viper.Unmarshal(AppConfig); err != nil { //将 Viper 读取的配置数据（如 config.yaml）反序列化到 AppConfig 结构体变量中。
		fmt.Printf("viper.Unmarshal failed,err:%v\n", err)
		// zap.L().Fatal("Error unmarshal Appconfig",zap.Error(err))
		return err
	}

	viper.WatchConfig()
	viper.OnConfigChange(func(in fsnotify.Event) {
		// fmt.Printf("配置文件被修改了...")
		zap.L().Info("配置文件被修改了", zap.String("fsnotify.Name", in.Name))
		if err := viper.Unmarshal(&AppConfig); err != nil {
			fmt.Printf("viper.Unmarshal failed,err:%v\n", err)
			// zap.L().Info("Error viper.unmarshal failed",zap.Error(err))
			return
		}
	})
	return nil
}
