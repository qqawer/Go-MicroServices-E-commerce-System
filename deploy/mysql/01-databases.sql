-- Only runs on the first initialization of an EMPTY MySQL volume.
CREATE DATABASE IF NOT EXISTS grpctest_goods_srv CHARACTER SET utf8mb4;
CREATE DATABASE IF NOT EXISTS grpctest_inventory_srv CHARACTER SET utf8mb4;
CREATE DATABASE IF NOT EXISTS grpctest_orders_srv CHARACTER SET utf8mb4;
GRANT ALL PRIVILEGES ON grpctest_goods_srv.* TO 'grpctest'@'%';
GRANT ALL PRIVILEGES ON grpctest_inventory_srv.* TO 'grpctest'@'%';
GRANT ALL PRIVILEGES ON grpctest_orders_srv.* TO 'grpctest'@'%';
