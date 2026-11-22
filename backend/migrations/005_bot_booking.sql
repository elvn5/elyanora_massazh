CREATE TABLE bot_sessions (
 chat_id bigint PRIMARY KEY,
 data jsonb NOT NULL DEFAULT '{}',
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- Transactional receipts prevent duplicate bookings when Telegram redelivers an update.
CREATE TABLE bot_updates (
 update_id bigint PRIMARY KEY,
 chat_id bigint NOT NULL,
 reply jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bookings_telegram ON bookings(telegram_chat,starts_at);
INSERT INTO schema_migrations(version) VALUES(5);
