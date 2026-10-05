package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"grpctest/goods_srv/global"
	"io"
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// Like mxshop, these hooks call ES before the MySQL transaction commits.
// An ES error is returned to GORM; there is no persistent queue or background retry.
func (g *Goods) AfterCreate(tx *gorm.DB) error { return g.writeES(tx) }
func (g *Goods) AfterUpdate(tx *gorm.DB) error { return g.writeES(tx) }

func (g *Goods) writeES(tx *gorm.DB) error {
	if global.EsClient == nil {
		return nil // The service configuration has not enabled ES synchronization.
	}
	if g.ID <= 0 {
		return fmt.Errorf("ES synchronization requires a product ID on the GORM model")
	}
	// Updates(map) can leave a partial model. Read the final row using this same
	// transaction, including false values and the stored FLOAT-to-decimal price.
	var doc EsGoods
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&Goods{}).
		Select(`id, name, goods_brief AS brief, category_id, brands_id AS brand_id,
			CAST(CAST(shop_price AS DECIMAL(18,2)) * 100 AS SIGNED) AS price_cents,
			on_sale, inventory_ready, ship_free, is_new, is_hot`).
		Where("id = ?", g.ID).Take(&doc).Error
	if err != nil {
		return fmt.Errorf("read product %d for ES: %w", g.ID, err)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	// A full document replaces the existing search copy or creates it if absent.
	return g.requestES(tx, http.MethodPut, body)
}

func (g *Goods) AfterDelete(tx *gorm.DB) error {
	if global.EsClient == nil {
		return nil
	}
	if g.ID <= 0 {
		return fmt.Errorf("ES deletion requires a product ID on the GORM model")
	}
	return g.requestES(tx, http.MethodDelete, nil)
}

func (g *Goods) requestES(tx *gorm.DB, method string, body []byte) error {
	ctx, cancel := context.WithTimeout(tx.Statement.Context, 5*time.Second)
	defer cancel()
	path := "/" + global.EsIndex + "/_doc/" + strconv.Itoa(int(g.ID))
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := global.EsClient.Perform(req)
	if err != nil {
		return fmt.Errorf("sync product %d to ES: %w", g.ID, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if method == http.MethodDelete && res.StatusCode == http.StatusNotFound {
		var result struct{ Result string }
		if json.Unmarshal(data, &result) == nil && result.Result == "not_found" {
			return nil // The document is already absent; a missing index still fails.
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("sync product %d to ES: HTTP %d: %s", g.ID, res.StatusCode, data)
	}
	return nil
}
