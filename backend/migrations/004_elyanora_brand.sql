-- Preserve custom messages while replacing the former studio name.
UPDATE bot_settings SET
  welcome_message=replace(replace(welcome_message,'Тихо','Elyanora'),'ТИХО','ELYANORA'),
  contact_text=replace(replace(contact_text,'Тихо','Elyanora'),'ТИХО','ELYANORA'),
  updated_at=now()
WHERE welcome_message LIKE '%Тихо%' OR welcome_message LIKE '%ТИХО%'
   OR contact_text LIKE '%Тихо%' OR contact_text LIKE '%ТИХО%';
INSERT INTO schema_migrations(version) VALUES(4);
