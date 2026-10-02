package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// This standalone module checks client compatibility without changing goods_srv.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	es, err := elasticsearch.NewBaseClient(elasticsearch.Config{
		Addresses: []string{"http://127.0.0.1:9201"},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer es.Close(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		log.Fatal(err)
	}
	res, err := es.Perform(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	var info struct {
		ClusterName string                  `json:"cluster_name"`
		Version     struct{ Number string } `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		log.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || info.ClusterName != "grpctest-es-lab" || info.Version.Number != "8.19.22" {
		log.Fatalf("unexpected lab response: HTTP %d, %+v", res.StatusCode, info)
	}
	fmt.Printf("PASS: Go client %s → ES %s (%s)\n", elasticsearch.Version, info.Version.Number, info.ClusterName)
}
