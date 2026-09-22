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
type InventoryConfig struct {
	Name string `mapstructure:"name" json:"name"`
}

type ServerConfig struct {
	InventoryInfo InventoryConfig `mapstructure:"inventory_srv" json:"inventory_srv"`
	//Host string `mapstructure:"host" json:"host"`
	//Port int `mapstructure:"port" json:"port"`
	//最后都会放到配置中心nacos去配置，这里就不配置这么多了
	Name       string       `mapstructure:"name" json:"name"` //给consul用的，服务发现通过这个name来拿到服务
	Host       string       `mapstructure:"host" json:"host"`
	Tags       []string     `mapstructure:"tags" json:"tags"`
	MysqlInfo  MysqlConfig  `mapstructure:"mysql" json:"mysql"`
	ConsulInfo ConsulConfig `mapstructure:"consul" json:"consul"`
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
