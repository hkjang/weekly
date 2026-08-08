# 운영 및 보안 가이드

## 필수 구성

- PostgreSQL 15 이상
- Weekly 서비스 이미지
- `/var/lib/weekly` 영속 볼륨
- 운영 TLS를 종료할 Reverse Proxy, Ingress 또는 Route

Weekly 프로세스에는 세 환경변수만 전달한다. DSN에는 `sslmode=require` 또는 사내 CA 검증이 가능한 `verify-full`을 권장한다.

## 백업

같은 복구 시점의 아래 두 대상을 함께 백업한다.

1. PostgreSQL 데이터베이스
2. `/var/lib/weekly` 볼륨

볼륨의 `instance.key`가 없으면 DB의 OIDC Client Secret을 복호화할 수 없다. 복구 후 `/readyz`, 로컬 관리자 로그인, OIDC 연결 시험, 샘플 PPTX 내보내기를 점검한다.

## 보안 통제

- 비루트 컨테이너, 모든 Linux capability 제거, read-only root filesystem
- HttpOnly/SameSite 세션 쿠키와 동일 출처 변경 요청 검사
- CSP, frame 차단, MIME sniffing 차단
- OIDC state/nonce/PKCE와 10분 만료
- 요청 본문 크기 제한: JSON 2MB, PPTX 25MB
- 감사 대상: 로그인, 보고서 상태/내용, 사용자/조직/설정, 키, 템플릿, PPTX 다운로드
- 개인 키 원문은 발급 응답에서 한 번만 노출

외부 Reverse Proxy를 사용할 때 신뢰할 수 없는 클라이언트가 `X-Forwarded-For`와 `X-Forwarded-Proto`를 직접 주입하지 못하도록 Proxy에서 해당 헤더를 덮어써야 한다.

## 상태 확인

- `GET /healthz`: 프로세스 생존
- `GET /readyz`: PostgreSQL 연결 포함 준비 상태
- 관리자 `보고 · 서비스 분석`: 최근 24시간 경로별 호출, 평균/최대 지연, 4xx/5xx 비율

## 업그레이드

1. DB와 상태 볼륨을 백업한다.
2. Release의 SHA-256을 확인한다.
3. `docker load` 후 Compose/Kubernetes 이미지 태그를 변경한다.
4. Weekly 시작 시 자동 마이그레이션이 완료되는지 로그를 확인한다.
5. `/readyz`와 로그인·보고서 조회를 점검한다.

롤백 시 스키마 하위 호환성이 보장된 버전만 사용한다. 현재 마이그레이션은 자동 down을 수행하지 않는다.
