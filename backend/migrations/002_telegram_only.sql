-- Stop collecting email and remove obsolete delivery jobs on existing installations.
DELETE FROM jobs WHERE channel <> 'telegram';
ALTER TABLE bookings DROP COLUMN email;
ALTER TABLE jobs ADD CONSTRAINT telegram_only CHECK(channel='telegram');
INSERT INTO schema_migrations(version) VALUES(2);
