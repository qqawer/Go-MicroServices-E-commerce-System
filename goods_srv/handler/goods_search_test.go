package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"grpctest/goods_srv/global"
	"grpctest/goods_srv/handler"
	"grpctest/goods_srv/model/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/elastic/go-elasticsearch/v8"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func searchES(t *testing.T, fn http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		fn(w, r)
	}))
	client, err := elasticsearch.NewBaseClient(elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true})
	if err != nil {
		t.Fatal(err)
	}
	oldClient, oldIndex := global.EsClient, global.EsIndex
	global.EsClient, global.EsIndex = client, "goods-search-test"
	t.Cleanup(func() {
		global.EsClient, global.EsIndex = oldClient, oldIndex
		client.Close(context.Background())
		server.Close()
	})
}

func TestStoreSearchRestoresESOrderAfterSQL(t *testing.T) {
	mock := batchGoodsMockDB(t)
	searchES(t, func(w http.ResponseWriter, r *http.Request) {
		var query struct {
			From, Size int
			Track      bool `json:"track_total_hits"`
		}
		if r.URL.Path != "/goods-search-test/_search" || json.NewDecoder(r.Body).Decode(&query) != nil || query.From != 2 || query.Size != 2 || !query.Track {
			t.Errorf("unexpected page request: %s %+v", r.URL, query)
		}
		fmt.Fprint(w, `{"hits":{"total":{"value":4,"relation":"eq"},"hits":[{"_id":"9"},{"_id":"3"}]}}`)
	})
	mock.ExpectQuery("SELECT .* FROM `goods`").WithArgs(true, true, 9, 3).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "on_sale", "inventory_ready"}).AddRow(3, "less relevant", true, true).AddRow(9, "most relevant", true, true))
	r, err := (&handler.GoodsServer{}).GoodsList(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true, Pages: 2, PagePerNums: 2})
	if err != nil || !r.SearchIndex || r.Total != 4 || len(r.Data) != 2 || r.Data[0].Id != 9 || r.Data[1].Id != 3 {
		t.Fatalf("ES order lost: %v %v", r, err)
	}
}

func TestStoreSearchRejectsFailedOrPartialResultsWithoutSQLFallback(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		body string
	}{
		{"unavailable", 503, `{}`}, {"invalid JSON", 200, `{`},
		{"timeout", 200, `{"timed_out":true,"hits":{"total":{"value":0,"relation":"eq"}}}`},
		{"failed shard", 200, `{"_shards":{"failed":1},"hits":{"total":{"value":0,"relation":"eq"}}}`},
		{"inexact total", 200, `{"hits":{"total":{"value":10000,"relation":"gte"}}}`},
		{"invalid ID", 200, `{"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_id":"bad"}]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			batchGoodsMockDB(t) // No SQL is permitted after a failed ES request.
			searchES(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.code); fmt.Fprint(w, test.body) })
			r, err := (&handler.GoodsServer{}).GoodsList(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true})
			if r != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("failure became success: %v %v", r, err)
			}
		})
	}
}

func TestStoreSearchEmptyAndInvalidRequestsAvoidSQL(t *testing.T) {
	batchGoodsMockDB(t)
	calls := 0
	searchES(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)
	})
	s := &handler.GoodsServer{}
	r, err := s.GoodsList(context.Background(), &proto.GoodsFilterRequest{StoreVisibleOnly: true, KeyWords: "empty"})
	if err != nil || r.Total != 0 || r.Data == nil || len(r.Data) != 0 {
		t.Fatalf("empty result: %v %v", r, err)
	}
	for _, req := range []*proto.GoodsFilterRequest{{Pages: -1}, {Pages: 2147483647}, {PriceMin: 20, PriceMax: 10}, {Brand: -1}} {
		req.StoreVisibleOnly = true
		if _, err := s.GoodsList(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GoodsList(ctx, &proto.GoodsFilterRequest{StoreVisibleOnly: true}); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("invalid requests reached ES: %d", calls)
	}
}
