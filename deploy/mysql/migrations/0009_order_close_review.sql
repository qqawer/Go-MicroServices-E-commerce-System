-- Missing/unknown trades are not payment evidence. Keep a durable recovery item
-- and a single resumable operator intent without storing a buyer ID or secrets.
CREATE TABLE grpctest_orders_srv.order_close_review (
  order_sn VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  reason VARCHAR(64) NOT NULL,
  attempts BIGINT NOT NULL DEFAULT 0,
  first_seen_at DATETIME(3) NOT NULL,
  last_checked_at DATETIME(3) NOT NULL,
  next_check_at DATETIME(3) NOT NULL,
  resolved_at DATETIME(3) NULL,
  resolution VARCHAR(64) NOT NULL DEFAULT '',
  request_id VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  operator VARCHAR(100) NOT NULL DEFAULT '',
  request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  app_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  seller_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  action_started_at DATETIME(3) NULL,
  action_expires_at DATETIME(3) NULL,
  KEY idx_close_review_pending (resolved_at, next_check_at, order_sn)
) ENGINE=InnoDB;
