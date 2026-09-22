-- Extend the existing transaction authority; old decisions have no request key.
ALTER TABLE grpctest_orders_srv.order_transaction
  ADD user_id INT NOT NULL DEFAULT 0,
  ADD request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  ADD request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  ADD snapshot JSON NULL,
  ADD expires_at DATETIME(3) NULL,
  ADD stock_released BOOLEAN NOT NULL DEFAULT FALSE,
  ADD UNIQUE KEY uq_order_request (user_id,request_key),
  ADD KEY idx_order_recovery (state,created_at),
  ADD KEY idx_order_expiry (state,expires_at);

-- New checkout calculations use integer cents. Keep old RPC float fields compatible.
ALTER TABLE grpctest_orders_srv.orderinfo ADD total_cents BIGINT NULL;
ALTER TABLE grpctest_orders_srv.ordergoods ADD unit_cents BIGINT NULL;
