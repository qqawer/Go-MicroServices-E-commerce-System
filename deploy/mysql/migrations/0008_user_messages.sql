-- User-owned text messages. Attachments and staff replies are separate features.
CREATE TABLE grpctest_user_srv.user_message (
  id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id INT NOT NULL,
  message_type INT NOT NULL,
  subject VARCHAR(100) NOT NULL,
  message VARCHAR(2000) NOT NULL,
  add_time DATETIME(3) NULL,
  update_time DATETIME(3) NULL,
  deleted_at DATETIME(3) NULL,
  is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
  KEY idx_user_message_owner (user_id, deleted_at, id),
  CONSTRAINT ck_user_message_owner CHECK (user_id > 0),
  CONSTRAINT ck_user_message_type CHECK (message_type BETWEEN 1 AND 5)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
