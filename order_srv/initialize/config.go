package initialize

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"grpctest/order_srv/config"
	"grpctest/order_srv/global"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/spf13/viper"
)

func GetEnvInfo(env string) bool {
	viper.AutomaticEnv()
	return viper.GetBool(env)
}

func InitConfig() error {
	configFileName := "../order_srv/config-pro.yaml"
	if GetEnvInfo("MXSHOP_DEBUG") {
		configFileName = "../order_srv/config-debug.yaml"
	}
	if override := os.Getenv("MXSHOP_CONFIG_FILE"); override != "" {
		configFileName = override
	}
	nacosConfig, err := readNacosConfig(configFileName)
	if err != nil {
		return err
	}
	serverConfig, err := loadServerConfig(nacosConfig, clients.CreateConfigClient)
	if err != nil {
		return err
	}
	// 只有完整配置通过校验后才发布，避免部分初始化的全局对象被使用。
	global.NacosConfig = nacosConfig
	global.ServerConfig = serverConfig
	return nil
}

func readNacosConfig(filename string) (config.NacosConfig, error) {
	v := viper.New()
	v.SetConfigFile(filename)
	var cfg config.NacosConfig
	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("read Nacos configuration: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("decode Nacos configuration: %w", err)
	}
	for _, field := range []struct{ name, value string }{
		{"host", cfg.Host}, {"dataid", cfg.DataID}, {"group", cfg.Group},
	} {
		if strings.TrimSpace(field.value) == "" {
			return cfg, fmt.Errorf("Nacos configuration %s is required", field.name)
		}
	}
	if cfg.Port == 0 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("Nacos configuration port must be between 1 and 65535")
	}
	return cfg, nil
}

func loadServerConfig(cfg config.NacosConfig, createClient func(map[string]interface{}) (config_client.IConfigClient, error)) (*config.ServerConfig, error) {
	client, err := createClient(map[string]interface{}{
		"serverConfigs": []constant.ServerConfig{{IpAddr: cfg.Host, Port: cfg.Port}},
		"clientConfig": constant.ClientConfig{
			Username: cfg.User, Password: cfg.Password, NamespaceId: cfg.Namespace,
			TimeoutMs: 5000, NotLoadCacheAtStart: true,
			LogDir: "./tmp/nacos/log", CacheDir: "./tmp/nacos/cache", LogLevel: "warn",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create Nacos configuration client: %w", err)
	}
	defer client.CloseClient()
	content, err := client.GetConfig(vo.ConfigParam{DataId: cfg.DataID, Group: cfg.Group})
	if err != nil {
		return nil, fmt.Errorf("read order service configuration from Nacos: %w", err)
	}
	return parseServerConfig(content)
}

func parseServerConfig(content string) (*config.ServerConfig, error) {
	var cfg *config.ServerConfig
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		return nil, fmt.Errorf("decode order service configuration: %w", err)
	}
	if err := validateServerConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validateServerConfig(cfg *config.ServerConfig) error {
	if cfg == nil {
		return fmt.Errorf("order service configuration must be a JSON object")
	}
	for _, field := range []struct{ name, value string }{
		{"name", cfg.Name}, {"host", cfg.Host},
		{"mysql.host", cfg.MysqlInfo.Host}, {"mysql.name", cfg.MysqlInfo.Name}, {"mysql.user", cfg.MysqlInfo.User},
		{"consul.host", cfg.ConsulInfo.Host},
		{"goods_srv.name", cfg.GoodsSerInfo.Name}, {"inventory_srv.name", cfg.InventorySerInfo.Name},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("order service configuration %s is required", field.name)
		}
	}
	if address := net.ParseIP(cfg.Host); address != nil && address.IsUnspecified() {
		return fmt.Errorf("order service configuration host must be a reachable address, not a wildcard")
	}
	mysqlPort, err := strconv.Atoi(cfg.MysqlInfo.Port)
	if err != nil || mysqlPort < 1 || mysqlPort > 65535 {
		return fmt.Errorf("order service configuration mysql.port must be between 1 and 65535")
	}
	if cfg.ConsulInfo.Port < 1 || cfg.ConsulInfo.Port > 65535 {
		return fmt.Errorf("order service configuration consul.port must be between 1 and 65535")
	}
	if err := cfg.MqInfo.Validate(true); err != nil {
		return err
	}
	if err := cfg.Alipay.Validate(); err != nil {
		return err
	}
	if cfg.MqInfo.Enabled && cfg.MqInfo.Mode == "alipay" && !cfg.Alipay.Enabled {
		return fmt.Errorf("rocket mode=alipay requires enabled sandbox payment configuration")
	}
	return nil
}
