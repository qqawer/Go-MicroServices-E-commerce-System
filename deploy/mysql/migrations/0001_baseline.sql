-- Frozen baseline: five goods tables from reviewed goods SQL and six business tables.

-- New changes belong in a NEW numbered file; do not edit applied migrations.

CREATE TABLE `grpctest_goods_srv`.`banner`  (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `add_time` datetime(0) NOT NULL,
  `is_deleted` tinyint(1) NULL DEFAULT NULL,
  `update_time` datetime(0) NOT NULL,
  `image` varchar(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `url` varchar(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `index` int(11) NOT NULL,
  `deleted_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_general_ci ROW_FORMAT = Dynamic;

CREATE TABLE `grpctest_goods_srv`.`brands`  (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `name` varchar(50) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `logo` varchar(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NULL DEFAULT NULL,
  `add_time` datetime(0) NOT NULL,
  `is_deleted` tinyint(1) NULL DEFAULT NULL,
  `update_time` datetime(0) NOT NULL,
  `deleted_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `brands_name`(`name`) USING BTREE
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_general_ci ROW_FORMAT = Dynamic;

CREATE TABLE `grpctest_goods_srv`.`category`  (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `name` varchar(50) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `parent_category_id` int(11) NULL DEFAULT NULL,
  `level` int(11) NOT NULL,
  `is_tab` tinyint(1) NOT NULL,
  `url` varchar(300) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `add_time` datetime(0) NULL DEFAULT NULL,
  `is_deleted` tinyint(255) NULL DEFAULT NULL,
  `update_time` datetime(0) NULL DEFAULT NULL,
  `deleted_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `category_name`(`name`) USING BTREE,
  INDEX `category_parent_category_id`(`parent_category_id`) USING BTREE,
  INDEX `category_url`(`url`) USING BTREE,
  CONSTRAINT `category_ibfk_1` FOREIGN KEY (`parent_category_id`) REFERENCES `grpctest_goods_srv`.`category` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_general_ci ROW_FORMAT = Dynamic;

CREATE TABLE `grpctest_goods_srv`.`goods`  (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `add_time` datetime(0) NOT NULL,
  `is_deleted` tinyint(1) NULL DEFAULT NULL,
  `update_time` datetime(0) NOT NULL,
  `category_id` int(11) NOT NULL,
  `brands_id` int(11) NOT NULL,
  `on_sale` tinyint(1) NOT NULL,
  `goods_sn` varchar(50) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `name` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `click_num` int(11) NOT NULL,
  `sold_num` int(11) NOT NULL,
  `fav_num` int(11) NOT NULL,
  `stocks` int(11) NOT NULL,
  `market_price` float NOT NULL,
  `shop_price` float NOT NULL,
  `goods_brief` varchar(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `ship_free` tinyint(1) NOT NULL,
  `images` json NOT NULL,
  `desc_images` json NOT NULL,
  `goods_front_image` varchar(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL,
  `is_new` tinyint(1) NOT NULL,
  `is_hot` tinyint(1) NOT NULL,
  `deleted_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `goods2_category_id`(`category_id`) USING BTREE,
  INDEX `goods2_brands_id`(`brands_id`) USING BTREE,
  CONSTRAINT `goods_ibfk_1` FOREIGN KEY (`category_id`) REFERENCES `grpctest_goods_srv`.`category` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT,
  CONSTRAINT `goods_ibfk_2` FOREIGN KEY (`brands_id`) REFERENCES `grpctest_goods_srv`.`brands` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_general_ci ROW_FORMAT = Dynamic;

CREATE TABLE `grpctest_goods_srv`.`goods_category_brand`  (
  `id` int(11) NOT NULL AUTO_INCREMENT,
  `category_id` int(11) NOT NULL,
  `brands_id` int(11) NOT NULL,
  `add_time` datetime(0) NOT NULL,
  `is_deleted` tinyint(1) NULL DEFAULT NULL,
  `update_time` datetime(0) NOT NULL,
  `deleted_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `goodscategorybrand_category_id_brands_id`(`category_id`, `brands_id`) USING BTREE,
  INDEX `goodscategorybrand_category_id`(`category_id`) USING BTREE,
  INDEX `goodscategorybrand_brands_id`(`brands_id`) USING BTREE,
  CONSTRAINT `goods_category_brand_ibfk_1` FOREIGN KEY (`category_id`) REFERENCES `grpctest_goods_srv`.`category` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `goods_category_brand_ibfk_2` FOREIGN KEY (`brands_id`) REFERENCES `grpctest_goods_srv`.`brands` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_general_ci ROW_FORMAT = Dynamic;
CREATE TABLE `grpctest_user_srv`.`user` (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  mobile varchar(100) NOT NULL, password varchar(100) NOT NULL,
  nick_name varchar(20), birthday datetime, gender varchar(6) DEFAULT 'male', role int DEFAULT 1,
  UNIQUE KEY idx_mobile (mobile), KEY idx_user_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `grpctest_inventory_srv`.`inventory` (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  goods int, stocks int, version int,
  KEY idx_inventory_goods (goods), KEY idx_inventory_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE `grpctest_inventory_srv`.`stockselldetail` (
  order_sn varchar(200), status varchar(200), detail varchar(200),
  UNIQUE KEY idx_order_sn (order_sn)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `grpctest_orders_srv`.`shoppingcart` (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  user int, goods int, nums int, checked boolean,
  KEY idx_shoppingcart_user (user), KEY idx_shoppingcart_goods (goods),
  KEY idx_shoppingcart_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE `grpctest_orders_srv`.`orderinfo` (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  user int, order_sn varchar(30), pay_type varchar(20), status varchar(20), trade_no varchar(100),
  order_mount float, pay_time datetime, address varchar(100), signer_name varchar(20),
  singer_mobile varchar(11), post varchar(20),
  KEY idx_orderinfo_user (user), KEY idx_orderinfo_order_sn (order_sn), KEY idx_orderinfo_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE `grpctest_orders_srv`.`ordergoods` (
  id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
  add_time datetime(3), update_time datetime(3), deleted_at datetime(3), is_deleted boolean,
  `order` int, goods int, goods_name varchar(100), goods_image varchar(200), goods_price float, nums int,
  KEY idx_ordergoods_order (`order`), KEY idx_ordergoods_goods (goods),
  KEY idx_ordergoods_goods_name (goods_name), KEY idx_ordergoods_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
