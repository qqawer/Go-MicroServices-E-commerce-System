-- Preserve existing order snapshots; preflight rejects invalid legacy records.
ALTER TABLE grpctest_inventory_srv.stockselldetail
  MODIFY order_sn VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  MODIFY status TINYINT NOT NULL,
  MODIFY detail JSON NOT NULL,
  ADD PRIMARY KEY (order_sn),
  ADD CONSTRAINT chk_stock_operation_status CHECK (status IN (0,1,2,3)),
  ADD CONSTRAINT chk_stock_operation_detail CHECK (JSON_TYPE(detail) = 'ARRAY');

-- Transaction decisions are the authority for RocketMQ transaction checks.
CREATE TABLE grpctest_orders_srv.order_transaction (
  order_sn VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  state TINYINT NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  CONSTRAINT chk_order_transaction_state CHECK (state IN (0,1,2))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Business changes and their pending events commit together. Redelivery is allowed.
CREATE TABLE grpctest_orders_srv.order_event (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  order_sn VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  topic VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  body JSON NOT NULL,
  delay_level INT NOT NULL,
  published BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY uq_order_event (order_sn,topic),
  KEY idx_order_event_pending (published,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

ALTER TABLE grpctest_orders_srv.orderinfo
  MODIFY order_sn VARCHAR(200) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  ADD UNIQUE KEY uq_orderinfo_order_sn (order_sn);

-- Existing persisted orders are committed creations, including orders later closed.
INSERT INTO grpctest_orders_srv.order_transaction (order_sn,state,created_at,updated_at)
SELECT order_sn,1,COALESCE(add_time,NOW(3)),COALESCE(update_time,NOW(3))
FROM grpctest_orders_srv.orderinfo;
