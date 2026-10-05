package initialize

import (
	"grpctest/goods_srv/config"
	"testing"
)

func TestESConfigurationRequiresExplicitTarget(t *testing.T) {
	if err := InitES(config.ElasticsearchConfig{}); err != nil {
		t.Fatalf("an existing configuration should keep ES disabled: %v", err)
	}
	for _, cfg := range []config.ElasticsearchConfig{
		{Enabled: true},
		{Enabled: true, URL: "http://localhost:9201", Index: "goods/*"},
	} {
		if err := InitES(cfg); err == nil {
			t.Fatalf("invalid enabled target accepted: %+v", cfg)
		}
	}
}
