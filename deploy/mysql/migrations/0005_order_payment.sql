-- Payment evidence is committed atomically with order status. No private key,
-- raw callback body or payer personal information is stored in this ledger.
CREATE TABLE grpctest_orders_srv.order_payment (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_sn VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  trade_no VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  app_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  seller_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  amount_cents BIGINT NOT NULL,
  status VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  notify_id VARCHAR(128) NOT NULL DEFAULT '',
  paid_at DATETIME(3) NULL,
  review BOOLEAN NOT NULL DEFAULT FALSE,
  review_reason VARCHAR(100) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  UNIQUE KEY uq_order_payment_order (order_sn),
  UNIQUE KEY uq_order_payment_trade (trade_no),
  KEY idx_order_payment_review (review, updated_at)
) ENGINE=InnoDB;

-- Failed/slow sends move behind untouched pending messages across process restarts.
ALTER TABLE grpctest_orders_srv.order_event
  ADD last_attempt_at DATETIME(3) NULL,
  ADD KEY idx_order_event_dispatch (published, last_attempt_at, id);
