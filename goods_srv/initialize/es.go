package initialize

import (
	"context"
	"encoding/json"
	"fmt"
	"grpctest/goods_srv/config"
	"grpctest/goods_srv/global"
	"grpctest/goods_srv/model"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// InitES is called once at startup. Existing configs keep sync disabled until
// they explicitly supply enabled=true, url and index.
func InitES(cfg config.ElasticsearchConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.URL == "" || !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(cfg.Index) {
		return fmt.Errorf("enabled Elasticsearch requires a URL and a concrete lowercase index name")
	}
	client, err := elasticsearch.NewBaseClient(elasticsearch.Config{
		Addresses: []string{cfg.URL}, DisableRetry: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureGoodsIndex(ctx, client, cfg.Index); err != nil {
		client.Close(context.Background())
		return err
	}
	global.EsClient, global.EsIndex = client, cfg.Index
	return nil
}

func ensureGoodsIndex(ctx context.Context, client *elasticsearch.BaseClient, index string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/"+index+"/_mapping", nil)
	if err != nil {
		return err
	}
	res, err := client.Perform(req)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(res.Body)
	res.Body.Close()
	if readErr != nil {
		return readErr
	}
	if res.StatusCode == http.StatusOK {
		type mappings struct {
			Properties map[string]any `json:"properties"`
		}
		var existing map[string]struct{ Mappings mappings }
		var wanted struct{ Mappings mappings }
		if err := json.Unmarshal(data, &existing); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(model.EsGoods{}.GetMapping()), &wanted); err != nil {
			return err
		}
		for field, definition := range wanted.Mappings.Properties {
			if !reflect.DeepEqual(existing[index].Mappings.Properties[field], definition) {
				return fmt.Errorf("index %s has incompatible mapping for %s", index, field)
			}
		}
		return nil
	}
	if res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("read ES mapping: HTTP %d: %s", res.StatusCode, data)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, "/"+index, strings.NewReader(model.EsGoods{}.GetMapping()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err = client.Perform(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err = io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("create goods index: HTTP %d: %s", res.StatusCode, data)
	}
	return nil
}
