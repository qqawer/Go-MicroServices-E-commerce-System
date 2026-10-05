// import-goods copies a stable local MySQL snapshot into a dedicated ES index.
// It does not synchronize later updates/deletes or change the storefront.
package main

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	_ "github.com/go-sql-driver/mysql"
)

//go:embed mapping.json
var mapping []byte

type goods struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Brief          string `json:"brief"`
	CategoryID     int64  `json:"category_id"`
	BrandID        int64  `json:"brand_id"`
	PriceCents     int64  `json:"price_cents"`
	OnSale         bool   `json:"on_sale"`
	InventoryReady *bool  `json:"inventory_ready"`
	ShipFree       bool   `json:"ship_free"`
	IsNew          bool   `json:"is_new"`
	IsHot          bool   `json:"is_hot"`
}

func main() {
	index := flag.String("index", "grpctest-goods-snapshot-20261005", "dedicated snapshot index")
	limit := flag.Int("limit", 0, "only import the first N goods for a separate smoke-test index; 0 means all")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, *index, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, index string, limit int) error {
	if !strings.HasPrefix(index, "grpctest-goods-snapshot-") || strings.ContainsAny(index, "/?*, ") || limit < 0 {
		return fmt.Errorf("use a dedicated grpctest-goods-snapshot-* index and a nonnegative limit")
	}
	dsn, endpoint := os.Getenv("MYSQL_DSN"), os.Getenv("ES_URL")
	if dsn == "" || endpoint == "" {
		return fmt.Errorf("MYSQL_DSN and ES_URL are required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("invalid MySQL configuration") // Do not log a DSN containing credentials.
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin MySQL read-only snapshot: %w", err)
	}
	defer tx.Rollback()
	var expected int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM goods WHERE deleted_at IS NULL").Scan(&expected); err != nil {
		return err
	}
	if limit > 0 && limit < expected {
		expected = limit
	}
	es, err := elasticsearch.NewBaseClient(elasticsearch.Config{Addresses: []string{endpoint}})
	if err != nil {
		return err
	}
	defer es.Close(context.Background())

	// Reuse only indexes created by this importer. Never delete/rebuild an index.
	status, data, err := request(ctx, es, http.MethodGet, "/"+index+"/_mapping", nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		status, data, err = request(ctx, es, http.MethodPut, "/"+index, mapping)
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("create index: HTTP %d %s: %v", status, data, err)
		}
	} else {
		var existing map[string]struct {
			Mappings struct {
				Meta struct{ Lab string } `json:"_meta"`
			} `json:"mappings"`
		}
		if status != http.StatusOK || json.Unmarshal(data, &existing) != nil || existing[index].Mappings.Meta.Lab != "grpctest-goods-snapshot-v1" {
			return fmt.Errorf("refusing an unrecognized index: %s (HTTP %d)", index, status)
		}
	}

	started := time.Now()
	lastID, imported, batches := int64(0), 0, 0
	for imported < expected {
		size := min(100, expected-imported)
		// The source price is yuan stored as FLOAT. MySQL converts to DECIMAL
		// with two fractional digits (rounding), then integer cents; Go never
		// multiplies a float32 and truncates it. The snapshot itself is unchanged.
		rows, err := tx.QueryContext(ctx, `SELECT id, name, goods_brief, category_id, brands_id,
			CAST(CAST(shop_price AS DECIMAL(18,2)) * 100 AS SIGNED),
			on_sale, inventory_ready, ship_free, is_new, is_hot
			FROM goods WHERE deleted_at IS NULL AND id > ? ORDER BY id LIMIT ?`, lastID, size)
		if err != nil {
			return err
		}
		var batch []goods
		for rows.Next() {
			var doc goods
			var ready sql.NullBool
			if err := rows.Scan(&doc.ID, &doc.Name, &doc.Brief, &doc.CategoryID, &doc.BrandID,
				&doc.PriceCents, &doc.OnSale, &ready, &doc.ShipFree, &doc.IsNew, &doc.IsHot); err != nil {
				rows.Close()
				return err
			}
			if ready.Valid {
				value := ready.Bool
				doc.InventoryReady = &value
			}
			batch = append(batch, doc)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) != size {
			return fmt.Errorf("snapshot count mismatch: expected batch %d, got %d", size, len(batch))
		}
		var body bytes.Buffer
		encoder := json.NewEncoder(&body)
		for _, doc := range batch {
			// Encode writes the newline required after both NDJSON lines.
			if err := encoder.Encode(map[string]any{"index": map[string]string{"_id": strconv.FormatInt(doc.ID, 10)}}); err != nil {
				return err
			}
			if err := encoder.Encode(doc); err != nil {
				return err
			}
		}
		status, data, err := request(ctx, es, http.MethodPost, "/"+index+"/_bulk", body.Bytes())
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("bulk: HTTP %d %s: %v", status, data, err)
		}
		var result struct {
			Errors bool `json:"errors"`
			Items  []struct {
				Index struct {
					ID     string          `json:"_id"`
					Status int             `json:"status"`
					Error  json.RawMessage `json:"error"`
				} `json:"index"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if len(result.Items) != len(batch) {
			return fmt.Errorf("bulk item count mismatch")
		}
		failed := 0
		for i, item := range result.Items {
			if item.Index.ID != strconv.FormatInt(batch[i].ID, 10) || item.Index.Status < 200 || item.Index.Status >= 300 {
				failed++
				fmt.Fprintf(os.Stderr, "goods_id=%d status=%d error=%s\n", batch[i].ID, item.Index.Status, item.Index.Error)
			}
		}
		if result.Errors || failed != 0 {
			return fmt.Errorf("bulk contained failures; fix the reported data and rerun (same _id replaces documents)")
		}
		lastID = batch[len(batch)-1].ID
		imported += len(batch)
		batches++
		fmt.Printf("batch=%d documents=%d last_id=%d total=%d/%d\n", batches, len(batch), lastID, imported, expected)
	}
	status, data, err = request(ctx, es, http.MethodPost, "/"+index+"/_refresh", nil)
	var refresh struct {
		Shards struct{ Failed int } `json:"_shards"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &refresh) != nil || refresh.Shards.Failed != 0 {
		return fmt.Errorf("refresh: HTTP %d %s: %v", status, data, err)
	}
	status, data, err = request(ctx, es, http.MethodGet, "/"+index+"/_count", nil)
	var count struct {
		Count  int                  `json:"count"`
		Shards struct{ Failed int } `json:"_shards"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &count) != nil || count.Shards.Failed != 0 || count.Count != expected {
		return fmt.Errorf("count verification: expected=%d HTTP %d %s: %v", expected, status, data, err)
	}
	fmt.Printf("PASS index=%s imported=%d batches=%d count=%d copy_and_verify_elapsed=%s\n", index, imported, batches, count.Count, time.Since(started))
	return nil
}

func request(ctx context.Context, es *elasticsearch.BaseClient, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.HasSuffix(path, "/_bulk") {
		req.Header.Set("Content-Type", "application/x-ndjson")
	}
	res, err := es.Perform(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	return res.StatusCode, data, err
}
