package initialize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grpctest/order_srv/config"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

const validOrderConfig = `{
	"name":"orders-srv","host":"order-srv",
	"mysql":{"host":"mysql","port":"3306","name":"orders","user":"app","password":"test-only"},
	"consul":{"host":"consul","port":8500},
	"goods_srv":{"name":"goods-srv"},"inventory_srv":{"name":"inventory-srv"}
}`

func TestParseServerConfigWithoutTransactionDependencies(t *testing.T) {
	cfg, err := parseServerConfig(validOrderConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MysqlInfo.Port != "3306" || cfg.MysqlInfo.Name != "orders" || cfg.MqInfo.Host != "" {
		t.Fatal("existing MySQL schema or optional MQ fields changed")
	}
}

func TestParseServerConfigRejectsInvalidRequiredFields(t *testing.T) {
	tests := []struct{ name, content, field string }{
		{"null", "null", "JSON object"},
		{"empty object", "{}", "name"},
		{"malformed", "{", "decode"},
		{"blank name", strings.Replace(validOrderConfig, `"orders-srv"`, `" "`, 1), "name"},
		{"wildcard", strings.Replace(validOrderConfig, `"order-srv"`, `"0.0.0.0"`, 1), "host"},
		{"IPv6 wildcard", strings.Replace(validOrderConfig, `"order-srv"`, `"::"`, 1), "host"},
		{"mysql host", strings.Replace(validOrderConfig, `"host":"mysql"`, `"host":""`, 1), "mysql.host"},
		{"mysql name", strings.Replace(validOrderConfig, `"name":"orders"`, `"name":""`, 1), "mysql.name"},
		{"mysql user", strings.Replace(validOrderConfig, `"user":"app"`, `"user":""`, 1), "mysql.user"},
		{"mysql port", strings.Replace(validOrderConfig, `"3306"`, `"70000"`, 1), "mysql.port"},
		{"consul host", strings.Replace(validOrderConfig, `"host":"consul"`, `"host":""`, 1), "consul.host"},
		{"consul port", strings.Replace(validOrderConfig, "8500", "0", 1), "consul.port"},
		{"goods name", strings.Replace(validOrderConfig, `"goods-srv"`, `""`, 1), "goods_srv.name"},
		{"inventory name", strings.Replace(validOrderConfig, `"inventory-srv"`, `""`, 1), "inventory_srv.name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseServerConfig(tt.content)
			if cfg != nil || err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("want invalid %s, got config=%v err=%v", tt.field, cfg != nil, err)
			}
		})
	}
}

func TestReadNacosConfigPreservesFlatSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte("host: example.invalid\nport: 8848\nnamespace: ''\nuser: test-user\npassword: test-password\ndataid: orders-srv.json\ngroup: dev\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := readNacosConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "example.invalid" || cfg.Port != 8848 || cfg.Namespace != "" || cfg.User != "test-user" || cfg.Password != "test-password" || cfg.DataID != "orders-srv.json" || cfg.Group != "dev" {
		t.Fatal("flat Nacos schema was not preserved")
	}
}

type fakeConfigClient struct {
	config_client.IConfigClient
	content string
	err     error
	closed  int
	param   vo.ConfigParam
}

func (f *fakeConfigClient) GetConfig(param vo.ConfigParam) (string, error) {
	f.param = param
	return f.content, f.err
}
func (f *fakeConfigClient) CloseClient() { f.closed++ }

func TestLoadServerConfigClosesNacosClient(t *testing.T) {
	for _, tt := range []struct {
		name, content string
		err           error
		wantErr       bool
	}{
		{"success", validOrderConfig, nil, false},
		{"read error", "", errors.New("read unavailable"), true},
		{"invalid content", "null", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeConfigClient{content: tt.content, err: tt.err}
			nacos := config.NacosConfig{Host: "example.invalid", Port: 8848, Namespace: "test-ns", User: "test-user", Password: "test-password", DataID: "orders-srv.json", Group: "dev"}
			create := func(options map[string]interface{}) (config_client.IConfigClient, error) {
				cc := options["clientConfig"].(constant.ClientConfig)
				sc := options["serverConfigs"].([]constant.ServerConfig)
				if cc.Username != nacos.User || cc.Password != nacos.Password || cc.NamespaceId != nacos.Namespace || sc[0].IpAddr != nacos.Host || sc[0].Port != nacos.Port {
					t.Fatal("Nacos connection configuration changed")
				}
				return client, nil
			}
			_, err := loadServerConfig(nacos, create)
			if (err != nil) != tt.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if client.closed != 1 || client.param.DataId != nacos.DataID || client.param.Group != nacos.Group {
				t.Fatal("client must read the selected config and close exactly once")
			}
		})
	}
}

func TestLoadServerConfigReturnsClientCreationError(t *testing.T) {
	want := errors.New("client unavailable")
	_, err := loadServerConfig(config.NacosConfig{}, func(map[string]interface{}) (config_client.IConfigClient, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("want client error, got %v", err)
	}
}

func TestInitConfigUsesOverrideAndReturnsBootstrapErrors(t *testing.T) {
	t.Setenv("MXSHOP_CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))
	if err := InitConfig(); err == nil || !strings.Contains(err.Error(), "read Nacos configuration") {
		t.Fatalf("want missing override file error before opening any connection, got %v", err)
	}
}
