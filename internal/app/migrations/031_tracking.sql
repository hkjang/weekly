-- 방문 추적 스크립트: an administrator picks a tracker in 관리자 설정 and the
-- application shell carries it, under a policy that names a per-request nonce
-- instead of 'unsafe-inline'.
--
-- Seeded off. A fresh install and an upgraded one serve the same page and the
-- same policy they did before this row existed, and only an administrator's
-- deliberate change makes either differ. Momento is the first choice and the
-- same-origin proxy (/momento/*) is its default endpoint, so that the common
-- setup adds no outside origin to the policy at all.
INSERT INTO app_settings(key, value, secret) VALUES
 ('tracking.enabled', 'false', false),
 ('tracking.provider', 'none', false),
 ('tracking.momento_url', '', false),
 ('tracking.momento_site_id', '', false),
 ('tracking.momento_endpoint', 'PROXY', false),
 ('tracking.measurement_id', '', false),
 ('tracking.matomo_url', '', false),
 ('tracking.matomo_site_id', '', false),
 ('tracking.custom_snippet', '', false),
 ('tracking.allowed_hosts', '', false),
 ('tracking.placement', 'head', false)
ON CONFLICT (key) DO NOTHING;
