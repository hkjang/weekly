-- Rows already written with this deployment's own network in them.
--
-- The reason on a delivery is shown on 개인 설정 to the writer. What the relay
-- said about an address belongs there — "받는 주소를 릴레이가 거부했습니다:
-- 550 5.1.1 mailbox unavailable" tells somebody their address is wrong. What
-- Go said about our network does not: measured on a deployment, 67 rows read
-- "연결할 수 없습니다: dial tcp 10.20.0.25:25: connect: connection refused" or
-- "…lookup smtp.internal.example on 127.0.0.11:53: no such host", carrying the
-- relay's address, its port and the container's resolver to 61 ordinary
-- writers who can do nothing with any of it.
--
-- The code stopped writing those. A delivery row lives as long as the report,
-- so without this the ones already there stay on those screens for good.
UPDATE report_mail_deliveries
   SET error_message = '릴레이에 연결하지 못했습니다. 잠시 뒤 다시 시도합니다.'
 WHERE error_message <> ''
   AND (error_message LIKE '%dial %' OR error_message LIKE '%no such host%'
        OR error_message LIKE '%connection refused%' OR error_message LIKE '%i/o timeout%'
        OR error_message LIKE '%network is unreachable%');

UPDATE report_mail_deliveries
   SET error_message = '릴레이와 암호화 연결을 맺지 못했습니다. 관리자에게 알려 주세요.'
 WHERE error_message <> ''
   AND (error_message LIKE '%x509%' OR error_message LIKE '%certificate%' OR error_message LIKE '%tls:%');

UPDATE report_mail_deliveries
   SET error_message = '릴레이가 계정 인증을 거부했습니다. 관리자에게 알려 주세요.'
 WHERE error_message LIKE '계정 인증에 실패했습니다%';
