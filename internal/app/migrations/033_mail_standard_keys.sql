-- The SMTP settings take the names the company-wide mail standard uses
-- (MAIL-STANDARD.md: mail.smtp_host, mail.smtp_port, mail.from_address,
-- lower-case security values with `auto`, mail.skip_tls_verify), so an
-- operator who has set up one service does not learn a second vocabulary here.
--
-- Values move with their rows: a relay that was configured keeps working
-- without anybody retyping it, and the encrypted password is untouched
-- because its key was already the standard's.
INSERT INTO app_settings(key, value, secret, updated_by, updated_at)
SELECT 'mail.smtp_host', value, secret, updated_by, updated_at FROM app_settings WHERE key = 'mail.host'
ON CONFLICT (key) DO NOTHING;
INSERT INTO app_settings(key, value, secret, updated_by, updated_at)
SELECT 'mail.smtp_port', value, secret, updated_by, updated_at FROM app_settings WHERE key = 'mail.port'
ON CONFLICT (key) DO NOTHING;
INSERT INTO app_settings(key, value, secret, updated_by, updated_at)
SELECT 'mail.from_address', value, secret, updated_by, updated_at FROM app_settings WHERE key = 'mail.from'
ON CONFLICT (key) DO NOTHING;
DELETE FROM app_settings WHERE key IN ('mail.host', 'mail.port', 'mail.from');

-- Security is spelled the standard's way. A deployment that never turned mail
-- on gets the standard's default, `auto` (upgrade to STARTTLS when the relay
-- offers it); one that is already sending keeps the choice it made, because
-- `auto` against a relay with a private certificate would start failing.
UPDATE app_settings SET value = lower(value) WHERE key = 'mail.security';
UPDATE app_settings SET value = 'auto'
WHERE key = 'mail.security' AND value = 'none'
  AND NOT EXISTS (SELECT 1 FROM app_settings WHERE key = 'mail.enabled' AND value = 'true');

INSERT INTO app_settings(key, value, secret) VALUES
 ('mail.smtp_host', '', false),
 ('mail.smtp_port', '25', false),
 ('mail.from_address', '', false),
 ('mail.skip_tls_verify', 'false', false)
ON CONFLICT (key) DO NOTHING;
