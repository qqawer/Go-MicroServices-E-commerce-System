-- Address-book rows belong to the user service. Orders retain their own snapshot
-- and deliberately do not reference mutable address-book rows.
CREATE TABLE grpctest_user_srv.user_address (
  id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id INT NOT NULL,
  name VARCHAR(20) NOT NULL,
  mobile VARCHAR(11) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  province VARCHAR(20) NOT NULL,
  city VARCHAR(20) NOT NULL,
  district VARCHAR(20) NOT NULL,
  address VARCHAR(100) NOT NULL,
  add_time DATETIME(3) NULL,
  update_time DATETIME(3) NULL,
  deleted_at DATETIME(3) NULL,
  is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
  KEY idx_user_address_owner (user_id, deleted_at, id),
  CONSTRAINT ck_user_address_owner CHECK (user_id > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
