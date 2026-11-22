ALTER TABLE jobs DROP CONSTRAINT IF EXISTS telegram_only;
ALTER TABLE jobs ADD CONSTRAINT telegram_channel CHECK(channel IN ('telegram','telegram_topic'));
INSERT INTO schema_migrations(version) VALUES(6);
