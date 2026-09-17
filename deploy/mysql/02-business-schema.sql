-- Initial schema for the current RPC models. Applied only to empty demo databases.
-- Later inventory/order changes belong in new migrations, not edits to live tables here.
USE grpctest_user_srv;
CREATE TABLE `user` (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  mobile varchar(100) NOT NULL, password varchar(100) NOT NULL,
  nick_name varchar(20), birthday datetime, gender varchar(6) DEFAULT 'male', role int DEFAULT 1,
  UNIQUE KEY idx_mobile (mobile), KEY idx_user_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

USE grpctest_inventory_srv;
CREATE TABLE inventory (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  goods int, stocks int, version int,
  KEY idx_inventory_goods (goods), KEY idx_inventory_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE stockselldetail (
  order_sn varchar(200), status varchar(200), detail varchar(200),
  UNIQUE KEY idx_order_sn (order_sn)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

USE grpctest_orders_srv;
CREATE TABLE shoppingcart (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  user int, goods int, nums int, checked boolean,
  KEY idx_shoppingcart_user (user), KEY idx_shoppingcart_goods (goods),
  KEY idx_shoppingcart_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE orderinfo (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  user int, order_sn varchar(30), pay_type varchar(20), status varchar(20), trade_no varchar(100),
  order_mount float, pay_time datetime, address varchar(100), signer_name varchar(20),
  singer_mobile varchar(11), post varchar(20),
  KEY idx_orderinfo_user (user), KEY idx_orderinfo_order_sn (order_sn), KEY idx_orderinfo_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE ordergoods (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  `order` int, goods int, goods_name varchar(100), goods_image varchar(200), goods_price float, nums int,
  KEY idx_ordergoods_order (`order`), KEY idx_ordergoods_goods (goods),
  KEY idx_ordergoods_goods_name (goods_name), KEY idx_ordergoods_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
