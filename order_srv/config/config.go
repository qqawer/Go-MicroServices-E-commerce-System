package config

type MysqlConfig struct {
	Host     string `mapstructure:"host" json:"host"`
	Port     string `mapstructure:"port" json:"port"`
	Name     string `mapstructure:"name" json:"name"`
	User     string `mapstructure:"user" json:"user"`
	Password string `mapstructure:"password" json:"password"`
}
type ConsulConfig struct {
	Host string `mapstructure:"host" json:"host"`
	Port int    `mapstructure:"port" json:"port"`
}

// GoodsSerConfig 映射商品配置
type GoodsSrvConfig struct {
	Name string `mapstructure:"name" json:"name"`
}

type MqConfig struct {
	Host      string `mapstructure:"host" json:"host"`
	Port      int    `mapstructure:"port" json:"port"`
	GroupName string `mapstructure:"group_name" json:"group_name"`
	Topic     string `mapstructure:"topic" json:"topic"`
}

type ServerConfig struct {
	//Host string `mapstructure:"host" json:"host"`
	//Port int `mapstructure:"port" json:"port"`
	//最后都会放到配置中心nacos去配置，这里就不配置这么多了
	Name       string       `mapstructure:"name" json:"name"` //给consul用的，服务发现通过这个name来拿到服务
	Host       string       `mapstructure:"host" json:"host"`
	Tags       []string     `mapstructure:"tags" json:"tags"`
	MysqlInfo  MysqlConfig  `mapstructure:"mysql" json:"mysql"`
	ConsulInfo ConsulConfig `mapstructure:"consul" json:"consul"`

	//商品微服务
	GoodsSerInfo GoodsSrvConfig `mapstructure:"goods_srv" json:"goods_srv"`
	//库存微服务
	InventorySerInfo GoodsSrvConfig `mapstructure:"inventory_srv" json:"inventory_srv"`
	MqInfo           MqConfig       `mapstructure:"rocket" json:"rocket"`
}
type NacosConfig struct {
	Host      string `mapstructure:"host" json:"host"`
	Port      uint64 `mapstructure:"port" json:"port"`
	Namespace string `mapstructure:"namespace" json:"namespace"`
	User      string `mapstructure:"user" json:"user"`
	Password  string `mapstructure:"password" json:"password"`
	DataID    string `mapstructure:"dataid" json:"dataid"`
	Group     string `mapstructure:"group" json:"group"`
}
