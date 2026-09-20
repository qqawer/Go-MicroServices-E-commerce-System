-- Test-only fixtures. Applied once transactionally; repeat seed never resets business data.
INSERT INTO grpctest_user_srv.user
(id,add_time,update_time,is_deleted,mobile,password,nick_name,gender,role)
VALUES (1,'2026-01-01','2026-01-01',0,'13000000001','test-only-not-a-password-hash','integration_user','male',1);
INSERT INTO grpctest_goods_srv.brands
(id,name,logo,add_time,update_time,is_deleted)
VALUES (1,'Test brand','https://example.invalid/brand.png','2026-01-01','2026-01-01',0);
INSERT INTO grpctest_goods_srv.category
(id,name,parent_category_id,level,is_tab,url,add_time,update_time,is_deleted)
VALUES (1,'Test category',NULL,1,0,'https://example.invalid/category','2026-01-01','2026-01-01',0);
INSERT INTO grpctest_goods_srv.goods_category_brand
(id,category_id,brands_id,add_time,update_time,is_deleted)
VALUES (1,1,1,'2026-01-01','2026-01-01',0);
INSERT INTO grpctest_goods_srv.goods
(id,category_id,brands_id,on_sale,ship_free,is_new,is_hot,name,goods_sn,click_num,sold_num,fav_num,
 market_price,shop_price,goods_brief,images,desc_images,goods_front_image,stocks,add_time,update_time,is_deleted)
VALUES (421,1,1,1,0,0,0,'Integration goods','TEST-421',0,0,0,19.5,12.5,'Test fixture',
 '["https://example.invalid/goods.png"]','[]','https://example.invalid/goods.png',100,'2026-01-01','2026-01-01',0);
INSERT INTO grpctest_inventory_srv.inventory
(id,add_time,update_time,is_deleted,goods,stocks,version)
VALUES (1,'2026-01-01','2026-01-01',0,421,100,0);
INSERT INTO grpctest_orders_srv.shoppingcart
(id,add_time,update_time,is_deleted,user,goods,nums,checked)
VALUES (1,'2026-01-01','2026-01-01',0,1,421,2,1);
