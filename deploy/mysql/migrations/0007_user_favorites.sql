-- User-owned favorite relationships. Product deletion leaves a removable
-- relationship; the product service supplies current availability when listed.
CREATE TABLE grpctest_user_srv.user_favorite (
  id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id INT NOT NULL,
  goods_id INT NOT NULL,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY uq_user_favorite_goods (user_id, goods_id),
  KEY idx_user_favorite_page (user_id, id),
  CONSTRAINT ck_user_favorite_owner CHECK (user_id > 0),
  CONSTRAINT ck_user_favorite_goods CHECK (goods_id > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
