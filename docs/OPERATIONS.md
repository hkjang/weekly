# 운영 및 보안 가이드

## 필수 구성

- PostgreSQL 15 이상
- Weekly 서비스 이미지
- `/var/lib/weekly` 영속 볼륨
- 운영 TLS를 종료할 Reverse Proxy, Ingress 또는 Route

Confluence 자동화를 사용하는 경우 Confluence Server 6.9.1 Base URL과 읽기 전용 Service Account, 선택된 Space에서 사내 AI Gateway로의 정책상 허용 여부도 사전에 확인한다. 연동값은 모두 관리자 UI에 저장한다.

Weekly 프로세스에는 세 필수 환경변수와 운영에서 강력히 권장하는 `WEEKLY_ENCRYPTION_KEY`를 전달한다. 암호화 키는 `openssl rand -base64 32`로 한 번 생성하고 Secret Manager나 Kubernetes Secret에 보관한 뒤 모든 업그레이드에서 같은 값을 유지한다. DSN에는 `sslmode=require` 또는 사내 CA 검증이 가능한 `verify-full`을 권장한다.

## 백업

같은 복구 시점의 아래 두 대상을 함께 백업한다.

1. PostgreSQL 데이터베이스
2. `/var/lib/weekly` 볼륨

`WEEKLY_ENCRYPTION_KEY`를 설정한 환경은 DB와 같은 키를 복구하면 OIDC Client Secret, AI API Key와 Confluence 비밀번호를 다시 입력하지 않아도 된다. 환경 키가 없는 하위 호환 모드에서는 볼륨의 `instance.key`가 유일한 복호화 키다. `imports/`에는 보관 중인 과거 PPTX 원본이 있다. 복구 후 `/readyz`, 로컬 관리자 로그인, 관리자 비밀값 상태, OIDC/AI/Confluence 연결 시험, 샘플 PPTX 내보내기와 Import 조회를 점검한다.

`scripts/weekly-backup.sh`가 두 대상을 한 번에 처리한다.

```
weekly-backup.sh backup  -o OUT_DIR  [-d DSN] [-s STATE_DIR]
weekly-backup.sh verify  -i ARCHIVE_DIR
weekly-backup.sh restore -i ARCHIVE_DIR [-d DSN] [-s STATE_DIR] [--force]
```

DSN은 `WEEKLY_POSTGRES_DSN`, 상태 볼륨은 `WEEKLY_STATE_DIR`(기본 `/var/lib/weekly`)에서 읽는다. Compose 환경은 볼륨을 마운트한 임시 컨테이너에서 실행한다.

```
docker run --rm -v weekly-data:/var/lib/weekly -v "$PWD/backups:/backups" \
  --network <weekly-network> -e WEEKLY_POSTGRES_DSN=... postgres:16-alpine \
  sh /backups/weekly-backup.sh backup -o /backups
```

**순서에 의미가 있다.** DB를 먼저 덤프하고 파일을 나중에 복사한다. 업로드는 파일을 먼저 쓰고 행을 나중에 넣으므로, 덤프가 본 행의 파일은 이미 디스크에 있다. 반대 순서로 하면 아직 기록되지 않은 파일을 가리키는 행이 백업된다. 남는 창은 백업 도중의 삭제 하나이며(행을 먼저 지우고 파일을 나중에 지운다) `verify`가 개수 불일치로 보고한다. 시점을 정확히 맞춰야 하면 서비스를 잠시 멈춘다.

`backup`과 `verify`는 참조된 첨부 파일 수와 실제 보관된 파일 수를 비교하고, 부족하면 **경고와 함께 0이 아닌 종료 코드**를 낸다. 스케줄 백업이 깨진 복구 지점을 성공으로 기록하지 않게 하기 위한 것이다. 같은 파일을 두 행이 공유할 수 있으므로 비교 기준은 행 수가 아니라 서로 다른 저장 경로 수다.

`restore`는 대상 데이터베이스의 모든 테이블을 지우고 다시 만들며 상태 볼륨을 비우고 덮어쓴다. `--force` 없이 실행하면 데이터베이스 이름을 직접 입력해야 진행한다.

## 보안 통제

- 비루트 컨테이너, 모든 Linux capability 제거, read-only root filesystem
- 대용량 multipart 처리를 위한 512MiB 임시 `/tmp`; Compose는 tmpfs, Kubernetes는 `emptyDir` 사용
- HttpOnly/SameSite 세션 쿠키와 동일 출처 변경 요청 검사
- 로컬 로그인 실패 횟수 제한: 기본 15분 안에 계정당 10회를 넘으면 429로 차단하며 차단 중에는 올바른 비밀번호도 거부한다. 실패마다 응답이 0.25초에서 2초까지 점점 느려진다. 계정 유무와 무관하게 같은 응답을 돌려주므로 계정 존재 여부를 알아낼 수 없다. 실패 기록은 PostgreSQL에 남아 재기동해도 유지되며 하루가 지나면 정리된다
- IP당 제한(`auth.max_login_attempts_per_ip`)은 **기본 비활성**이다. 사무실 NAT이나 Reverse Proxy 뒤에서는 다수 사용자가 같은 주소로 보이므로, 의미 있는 임계값은 남의 오타로 무관한 사용자를 함께 잠근다. 클라이언트 주소가 실제로 구분되는 망에서만 켠다
- CSP, frame 차단, MIME sniffing 차단
- OIDC state/nonce/PKCE와 10분 만료
- 요청 본문 크기 제한: JSON 2MB, Import PPTX는 관리자 설정(기본 파일당 25MB·작업당 20개)
- AI 응답 2MB 제한, 호출 제한시간 5~300초, HTTP Redirect 차단
- HTTP 본문 읽기 5분·응답 쓰기 330초 제한으로 대량 업로드와 최대 AI 제한시간을 수용
- PPTX ZIP 엔트리·압축 해제 크기·슬라이드 XML 크기 제한으로 ZIP bomb 완화
- 감사 대상: 로그인, 로그인 차단, 업무 병합·분리, 보고서 상태/내용, 사용자/조직/설정, 키, 템플릿, PPTX 다운로드, AI 분석, Import 업로드·재분석·확정, Confluence Sync·자동 매핑·후보 수정/제외/수락
- 개인 키 원문은 발급 응답에서 한 번만 노출

외부 Reverse Proxy를 사용할 때 신뢰할 수 없는 클라이언트가 `X-Forwarded-For`와 `X-Forwarded-Proto`를 직접 주입하지 못하도록 Proxy에서 해당 헤더를 덮어써야 한다.

## 상태 확인

- `GET /healthz`: 프로세스 생존
- `GET /readyz`: PostgreSQL 연결 포함 준비 상태
- 관리자 `보고 · 서비스 분석`: 최근 24시간 경로별 호출, 평균/최대 지연, 4xx/5xx 비율

정시 제출 판정 기준은 관리자 설정의 `제출 마감`이다. `주차 시작일 + N일 + H시`를 서비스 시간대로 해석한 시각과 제출 시각을 비교하며, 기본값 7일 24시는 주차 시작일로부터 7일째 되는 날 자정이다. 보고 분석 화면이 이 기준을 그대로 표시한다.

Import Worker는 API 프로세스 내부에서 동작하며 PostgreSQL의 `FOR UPDATE SKIP LOCKED`로 작업을 하나씩 가져간다. 프로세스 재시작 시 `PROCESSING` 작업은 `PENDING/QUEUED`로 복구된다. 작업이 멈춘 것처럼 보이면 AI Gateway 연결과 모델의 JSON Schema 지원 여부, `/var/lib/weekly/imports` 쓰기 권한, Import 상세 오류를 순서대로 점검한다.

`import.retention_days`가 지난 `CONFIRMED`, `SKIPPED`, `FAILED` 원본은 30분 유지보수 주기에 최대 500개씩 제거된다. 분석 메타데이터는 삭제하지 않는다. 원본까지 장기 보존해야 한다면 관리자 설정에서 기간을 늘리고 DB와 볼륨을 같은 복구 시점으로 백업한다.

## 업그레이드

1. DB와 상태 볼륨을 백업한다. `scripts/weekly-backup.sh backup`을 쓰고 `verify`가 통과하는지 확인한다. 여기서 첨부 파일 경고가 나오면 업그레이드 전에 이미 볼륨이 어긋나 있다는 뜻이다.
2. 아직 환경 암호화 키가 없다면 기존 상태 볼륨을 유지한 채 `openssl rand -base64 32` 결과를 `WEEKLY_ENCRYPTION_KEY`에 설정한다. 첫 기동이 기존 비밀값을 자동 재암호화한다.
3. Release의 SHA-256을 확인한다.
4. `docker load` 후 Compose/Kubernetes 이미지 태그를 변경한다.
5. Weekly 시작 로그에서 `secret encryption initialized`와 자동 마이그레이션 완료를 확인한다.
6. `/readyz`, 로그인·보고서 조회와 관리자 비밀값의 `안전하게 설정됨` 상태를 점검한다.
7. AI를 사용하는 환경은 AI 연결 시험, 자유 텍스트 미리보기와 테스트 PPTX Import를 확인한다.
8. Confluence 자동화를 사용하는 환경은 REST 연결 시험, 사용자 매핑, 샘플 증분 Sync와 후보 출처를 확인한다.

롤백 시 스키마 하위 호환성이 보장된 버전만 사용한다. 현재 마이그레이션은 자동 down을 수행하지 않는다.
