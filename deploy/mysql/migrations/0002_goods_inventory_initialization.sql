-- Existing goods have demo inventory; new goods explicitly start unready.
ALTER TABLE grpctest_goods_srv.goods ADD COLUMN inventory_ready BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE grpctest_goods_srv.goods_creation (
  request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  goods_id INT NULL,
  initial_stock INT NOT NULL,
  requested_on_sale BOOLEAN NOT NULL,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY uq_goods_creation_goods (goods_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Preflight rejects duplicate/invalid inventory instead of guessing which row to keep.
ALTER TABLE grpctest_inventory_srv.inventory
  MODIFY goods INT NOT NULL,
  MODIFY stocks INT NOT NULL,
  ADD UNIQUE KEY uq_inventory_goods (goods),
  ADD CONSTRAINT chk_inventory_goods_positive CHECK (goods > 0),
  ADD CONSTRAINT chk_inventory_stock_nonnegative CHECK (stocks >= 0);

CREATE TABLE grpctest_inventory_srv.inventory_initialization (
  goods INT NOT NULL PRIMARY KEY,
  initial_stock INT NOT NULL,
  created_at DATETIME(3) NOT NULL,
  CONSTRAINT chk_initial_stock_nonnegative CHECK (initial_stock >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
