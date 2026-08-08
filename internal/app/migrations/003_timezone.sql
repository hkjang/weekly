INSERT INTO app_settings(key, value, secret)
VALUES ('service.timezone', 'Asia/Seoul', false)
ON CONFLICT (key) DO NOTHING;
