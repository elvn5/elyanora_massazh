CREATE TABLE bot_settings (
 id int PRIMARY KEY CHECK(id=1),
 welcome_message text NOT NULL CHECK(char_length(welcome_message) BETWEEN 2 AND 1000),
 contact_text text NOT NULL DEFAULT '' CHECK(char_length(contact_text)<=500),
 services_enabled boolean NOT NULL DEFAULT true,
 reminder_24h boolean NOT NULL DEFAULT true,
 reminder_2h boolean NOT NULL DEFAULT true,
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO bot_settings(id,welcome_message) VALUES(1,'Добро пожаловать в студию Тихо! Выберите удобное время для заботы о себе.');
INSERT INTO schema_migrations(version) VALUES(3);
