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

볼륨의 `instance.key`가 없으면 DB의 OIDC Client Secret과 AI API Key를 복호화할 수 없다. `imports/`에는 보관 중인 과거 PPTX 원본이 있다. 복구 후 `/readyz`, 로컬 관리자 로그인, OIDC/AI 연결 시험, 샘플 PPTX 내보내기와 Import 조회를 점검한다.

## 보안 통제

- 비루트 컨테이너, 모든 Linux capability 제거, read-only root filesystem
- 대용량 multipart 처리를 위한 512MiB 임시 `/tmp`; Compose는 tmpfs, Kubernetes는 `emptyDir` 사용
- HttpOnly/SameSite 세션 쿠키와 동일 출처 변경 요청 검사
- CSP, frame 차단, MIME sniffing 차단
- OIDC state/nonce/PKCE와 10분 만료
- 요청 본문 크기 제한: JSON 2MB, Import PPTX는 관리자 설정(기본 파일당 25MB·작업당 20개)
- AI 응답 2MB 제한, 호출 제한시간 5~300초, HTTP Redirect 차단
- HTTP 본문 읽기 5분·응답 쓰기 330초 제한으로 대량 업로드와 최대 AI 제한시간을 수용
- PPTX ZIP 엔트리·압축 해제 크기·슬라이드 XML 크기 제한으로 ZIP bomb 완화
- 감사 대상: 로그인, 보고서 상태/내용, 사용자/조직/설정, 키, 템플릿, PPTX 다운로드, AI 분석, Import 업로드·재분석·확정
- 개인 키 원문은 발급 응답에서 한 번만 노출

외부 Reverse Proxy를 사용할 때 신뢰할 수 없는 클라이언트가 `X-Forwarded-For`와 `X-Forwarded-Proto`를 직접 주입하지 못하도록 Proxy에서 해당 헤더를 덮어써야 한다.

## 상태 확인

- `GET /healthz`: 프로세스 생존
- `GET /readyz`: PostgreSQL 연결 포함 준비 상태
- 관리자 `보고 · 서비스 분석`: 최근 24시간 경로별 호출, 평균/최대 지연, 4xx/5xx 비율

Import Worker는 API 프로세스 내부에서 동작하며 PostgreSQL의 `FOR UPDATE SKIP LOCKED`로 작업을 하나씩 가져간다. 프로세스 재시작 시 `PROCESSING` 작업은 `PENDING/QUEUED`로 복구된다. 작업이 멈춘 것처럼 보이면 AI Gateway 연결과 모델의 JSON Schema 지원 여부, `/var/lib/weekly/imports` 쓰기 권한, Import 상세 오류를 순서대로 점검한다.

`import.retention_days`가 지난 `CONFIRMED`, `SKIPPED`, `FAILED` 원본은 30분 유지보수 주기에 최대 500개씩 제거된다. 분석 메타데이터는 삭제하지 않는다. 원본까지 장기 보존해야 한다면 관리자 설정에서 기간을 늘리고 DB와 볼륨을 같은 복구 시점으로 백업한다.

## 업그레이드

1. DB와 상태 볼륨을 백업한다.
2. Release의 SHA-256을 확인한다.
3. `docker load` 후 Compose/Kubernetes 이미지 태그를 변경한다.
4. Weekly 시작 시 자동 마이그레이션이 완료되는지 로그를 확인한다.
5. `/readyz`와 로그인·보고서 조회를 점검한다.
6. AI를 사용하는 환경은 AI 연결 시험, 자유 텍스트 미리보기와 테스트 PPTX Import를 확인한다.

롤백 시 스키마 하위 호환성이 보장된 버전만 사용한다. 현재 마이그레이션은 자동 down을 수행하지 않는다.
