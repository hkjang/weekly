<p align="center">
  <img src="docs/favicon.svg" alt="Weekly Logo" width="90"><br><br>
  <h1 align="center">Weekly</h1>
</p>

<p align="center">
  <strong>사내 Web/App을 위한 오프라인망 운영형 주간업무보고 관리 플랫폼</strong><br>
  개인 보고서 작성부터 팀장 승인, 원본 PPTX 보존 자동 내보내기까지 단일 패키지로 제공합니다.
</p>

<p align="center">
  <a href="https://hkjang.github.io/weekly/">🇰🇷 홍보 페이지</a> · <a href="https://hkjang.github.io/weekly/index_en.html">🇺🇸 English Page</a> · <a href="https://github.com/sponsors/hkjang">💖 Sponsor</a>
</p>

---

## 주요 기능

- `Report → ReportItem` 데이터 모델: 금주 실적, 차주 계획, 이슈, 진척도
- 일요일부터 토요일까지 선택 가능한 주차 시작 요일과 7일 단위 보고 기간
- 현재·과거 본인 보고서 수정 및 낙관적 잠금이 적용된 확인형 삭제
- 개인 화면과 관리자 관리 화면의 명확한 분리
- 관리자 설정에 따라 켜지는 팀장 검토·승인·반려 흐름
- Keycloak 호환 OIDC Discovery, Authorization Code + PKCE, nonce 검증
- 조직 계층 기반 RBAC: `USER`, `TEAM_LEADER`, `ORG_MANAGER`, `ADMIN`
- Argon2id 로컬 인증, DB 세션, 감사 로그, 낙관적 잠금
- 개인 API/MCP 키 발급·폐기 및 전체 즉시 폐기형 개인 키 회전
- 주간보고를 월간·분기·반기·연간으로 자동 취합하고 중복 업무와 반복 기재를 제거하는 기간 업무보고
- 완료율, 정체·이월 업무, 이슈 지속 업무와 보고 커버리지를 판단 근거와 함께 제시하는 경영 인사이트
- 주차별 상태 추이, 업무 타임라인, 포트폴리오 구성을 담은 업무 시각화 화면과 CSV·PPTX 내보내기
- 제출률, 이슈, 진척도와 최근 24시간 API 성능 분석
- REST API와 읽기 전용 Streamable HTTP MCP 서버
- 관리자가 등록한 원본 PPTX 규격을 보존하고 업무 내용량을 기준으로 슬라이드를 균형 배치하는 주간보고 내보내기
- 자유 텍스트의 독립 사실을 원자 항목으로 구조화해 한 줄씩 표시하는 AI 작성 미리보기
- 과거 PPTX의 슬라이드·표 구조, 업무 구분과 근거 슬라이드를 검증한 뒤 확인 후 적재하는 Import
- Confluence Server 6.9.1 활동 증분 수집, 입력 Page 근거 검증과 사용자별 AI 주간보고 자동 초안
- 로그인 화면과 프로필 컨텍스트 메뉴의 빌드 버전 표시
- 프론트엔드 정적 자산을 포함한 단일 오프라인 Docker 이미지

## 오프라인 설치

GitHub Release에서 `weekly-v<VERSION>.tar.gz` 하나만 반입합니다. 파일을 적재하면 동일한 버전의 `weekly:v<VERSION>` 이미지가 생성됩니다.

```bash
gzip -dc weekly-v0.7.0.tar.gz | docker load
cp deploy/.env.example deploy/.env
# 필수 세 값과 운영용 WEEKLY_ENCRYPTION_KEY를 설정
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d
```

Weekly는 세 개의 필수 환경변수와 한 개의 권장 보안 환경변수를 받습니다.

| 환경변수 | 설명 |
|---|---|
| `WEEKLY_POSTGRES_DSN` | PostgreSQL 연결 문자열 |
| `WEEKLY_BOOTSTRAP_ADMIN` | 최초 관리자 아이디 |
| `WEEKLY_BOOTSTRAP_ADMIN_PASSWORD` | 최초 관리자 비밀번호, 12자 이상 |
| `WEEKLY_ENCRYPTION_KEY` | 권장: `openssl rand -base64 32`로 한 번 생성해 업그레이드마다 유지할 비밀 설정 암호화 키 |

최초 관리자가 만들어진 뒤에는 환경변수의 비밀번호를 바꿔도 기존 관리자 비밀번호를 덮어쓰지 않습니다. `WEEKLY_ENCRYPTION_KEY`는 연결 비밀번호 자체가 아니라 관리자 화면에서 입력한 OIDC Client Secret, AI API Key와 Confluence 비밀번호를 보호하는 마스터 키입니다. 같은 값을 유지하면 컨테이너나 상태 볼륨이 교체돼도 PostgreSQL의 암호화 설정을 계속 복호화할 수 있습니다. 기존 `instance.key`를 쓰던 환경은 기존 볼륨을 연결한 첫 업그레이드에 이 값을 설정하면 자동으로 재암호화됩니다.

> `/var/lib/weekly` 볼륨과 `WEEKLY_ENCRYPTION_KEY`를 각각 백업하십시오. 환경 키를 설정하지 않은 하위 호환 모드에서는 볼륨의 `instance.key`가 유일한 복호화 키입니다.

## Keycloak OIDC 설정

1. 부트스트랩 관리자로 로그인합니다.
2. `관리자 설정 → 서비스 설정 → 인증 · Keycloak OIDC`로 이동합니다.
3. Issuer URL, Client ID, Client Secret을 입력합니다.
4. Keycloak의 Valid redirect URI에 `https://<weekly-host>/api/v1/auth/oidc/callback`을 등록합니다.
5. OIDC 사용을 켜고 저장한 다음 `OIDC 연결 시험`을 실행합니다.
6. SSO 로그인을 확인한 뒤 필요한 경우 로컬 로그인을 끕니다.

기본 사용자명 claim은 `preferred_username`, 그룹 claim은 `groups`입니다. 관리자 그룹을 지정하면 해당 그룹으로 로그인한 사용자를 `ADMIN`으로 승격할 수 있습니다. 자동 등록을 끄면 미리 생성된 Weekly 사용자만 SSO 로그인할 수 있습니다.

## 승인 워크플로

`workflow.enabled`의 기본값은 `false`입니다.

- 꺼짐: `DRAFT → CLOSED`. 검토·승인·반려 버튼과 절차가 제외됩니다.
- 켜짐: `DRAFT → SUBMITTED → APPROVED` 또는 `REVISION_REQUESTED → SUBMITTED`.

모든 상태 전이는 `report_status_history`와 감사 로그에 기록됩니다. 본인은 어느 상태의 보고서도 수정·삭제할 수 있습니다. `SUBMITTED` 또는 `APPROVED` 내용을 수정하면 기존 검토 효력을 유지하지 않고 자동으로 `DRAFT`로 돌아가며 상태 이력이 남습니다. `CLOSED` 보고서는 수정 후 확정 상태를 유지합니다.

과거 보고 상세의 `복제`를 누르면 원하는 새 주차에 항상 `DRAFT` 상태로 복제합니다. 권장 모드인 `업무 구조만`은 분류·제목만 옮기고 요약·실적·계획·이슈·진척도를 초기화하며, `전체 내용`은 편집을 전제로 모든 내용을 복사합니다. 승인 이력, 댓글, Import·Confluence 연결과 원본 항목 ID는 복제하지 않습니다.

관리자는 `workflow.week_start`에서 일요일부터 토요일까지 어느 요일이든 주차 시작일로 지정할 수 있습니다. 현재 주차, 분석, Confluence 후보, PPTX 날짜와 Import 날짜 보정이 같은 설정을 사용합니다.

## PPTX 내보내기

`1월5주간업무보고_AI엔지니어링.pptx`와 같은 4장 구성, 슬라이드 크기, 조직명, 추진실적·추진계획 표와 글꼴 규격의 기본 템플릿을 코드에서 생성합니다. 원본 파일이 빌드 컨텍스트에 있으면 원본 슬라이드의 마스터, 표, 도형, 글꼴과 이미지를 우선 그대로 보존합니다. 두 방식 모두 조직명, 추진실적·추진계획 일정과 이번 주/다음 주 업무만 자동으로 교체합니다. 기본 4장 형식은 보고 순서를 유지하면서 실적·계획 중 더 긴 열, 줄바꿈과 제목·구분 높이를 계산해 내용량을 균형 분배하며, 같은 구분이 여러 장으로 나뉘면 각 장에 구분 제목을 다시 표시합니다. 따라서 많은 구분이 마지막 장에 몰리거나 큰 구분 전체가 한 장을 넘치는 문제를 줄이면서 항목을 정확히 한 번씩 출력합니다.

다른 규격이 필요하면 `관리자 설정 → PPTX 템플릿`에서 토큰 방식 PPTX를 등록할 수 있습니다. 이때도 원본 구성은 그대로 두고 아래 독립 텍스트 상자만 바꿉니다.

| 토큰 | 치환 내용 |
|---|---|
| `{{WEEK_SCHEDULE}}` | 주간 일정 범위 |
| `{{THIS_WEEK}}` | 업무별 이번 주 한 일 |
| `{{NEXT_WEEK}}` | 업무별 다음 주 할 일 |
| `{{ISSUES}}` | 이슈·지원 요청 |
| `{{AUTHOR}}` | 작성자 |
| `{{TEAM}}` | 조직명 |
| `{{WEEK_START}}`, `{{WEEK_END}}` | 개별 시작·종료일 |

`{{THIS_WEEK}}`와 `{{NEXT_WEEK}}`는 필수이며, 각 토큰은 PowerPoint에서 하나의 독립된 텍스트 상자/텍스트 런으로 둬야 합니다. 등록 시 ZIP 구조와 필수 구성요소를 검증합니다. 보고서 화면과 과거/팀 보고 화면의 `PPTX 다운로드`에서 결과를 받습니다.

## AI 작성과 과거 PPTX 가져오기

관리자가 `관리자 설정 → AI · Import`에서 OpenAI 호환 Chat Completions 전체 Endpoint, 모델, API Key(선택)를 저장하고 연결 시험을 통과시킨 뒤 AI 기능을 켭니다. 예를 들어 Endpoint는 `http://ai-gateway.internal/v1/chat/completions`처럼 호출 가능한 전체 주소를 입력합니다. AI 연결값은 계속 관리자 UI에서 관리하며 `WEEKLY_ENCRYPTION_KEY`에는 연결값이 아닌 암호화 키만 둡니다.

작성자는 보고서 편집 화면의 텍스트 영역에 이번 주 한 일, 다음 주 할 일과 이슈를 자유롭게 붙여 넣고 `AI 분석`을 실행합니다. 서버는 금주 실적·차주 계획·이슈를 자유 문자열이 아닌 원자 사실 배열로 받는 엄격한 JSON Schema Structured Output을 검증한 뒤, 공개 API와 편집기에는 한 줄 한 항목의 글머리표로 투영합니다. 같은 구분·제목 항목은 중복 줄을 제거해 병합하고 낮은 쪽 신뢰도를 유지합니다. 사용자는 항목과 신뢰도를 확인·수정한 후 `병합` 또는 `교체`를 선택해 편집기에 적용합니다. 같은 줄바꿈 구조가 PPTX에도 개별 세부 문단으로 반영됩니다. 적용만으로 DB에 저장되지 않고 기존 저장 버튼을 눌러야 반영됩니다.

`PPTX 가져오기`에서는 여러 과거 파일을 올릴 수 있습니다. 서버가 먼저 안전하게 Open XML을 열어 `presentation.xml` 관계에 따른 화면 표시 순서, 원본 `slideN.xml`, 도형 이름, 문단·글머리표 단계와 표의 행·열·빈 셀을 보존합니다. 한 문단이 여러 Text Run으로 나뉜 경우 다시 결합한 뒤 파일명과 본문에서 날짜를 결정적으로 찾고, 이 구조화 텍스트만 AI에 전달합니다. 입력 한도를 넘으면 앞 슬라이드만 남기지 않고 슬라이드별로 공정하게 예산을 나누며 일부 내용이 잘린 슬라이드 번호를 경고합니다. AI 항목의 `sourceSlides`가 실제 표시 순서 슬라이드인지 검사하고 업무 구분 신뢰도를 별도로 표시하며, 근거 슬라이드가 없거나 존재하지 않는 슬라이드를 가리키는 응답은 저장 후보로 사용하지 않습니다. 분석 결과는 비동기 작업으로 제공되며 사용자가 주차, 업무 순서와 항목을 고친 뒤 아래 전략으로 확정합니다.

| 전략 | 동작 |
|---|---|
| 새로 생성 | 동일 주차 보고가 없을 때 과거 보고를 생성 |
| 병합 | 동일 분류·제목 항목에 내용을 합치고 나머지는 추가 |
| 교체 | 기존 주차 항목 전체를 분석 결과로 교체 |
| 건너뛰기 | 분석 이력만 남기고 보고서는 생성하지 않음 |

동일 사용자의 같은 SHA-256 파일은 중복으로 표시합니다. 원본이 남은 미확정 `FAILED`, `READY`, `NEEDS_REVIEW` 파일은 파일별 `최신 파이프라인으로 다시 분석`할 수 있으므로 파서·프롬프트 개선 후 재업로드할 필요가 없습니다. `NEEDS_REVIEW` 결과는 기본 선택하지 않으며 날짜·업무 구분·근거 슬라이드를 직접 확인해야 합니다. 확정 시 주차 날짜가 관리자 시작 요일과 일치하는지도 다시 검사하고, 같은 구분·제목 항목은 중복 없이 병합합니다. 확정된 보고는 `PPTX_IMPORT`, AI 작성 후 저장된 보고는 `AI_TEXT` 출처가 기록됩니다. 원본 파일은 `import.retention_days` 이후 제거하되 해시, 추출 결과, AI 응답, 연결 보고서와 감사 이력은 DB에 보존됩니다.

## Confluence 6.9.1 자동 초안

관리자는 `관리자 관리 → 서비스 설정 → Confluence 6.9.1 자동화`에서 Base URL, 연동 전용 Service Account, 포함·제외 Space, 수집 주기, 규칙 점수와 AI/본문 분석 정책을 저장합니다. Confluence 6.9.1은 Personal Access Token을 전제로 하지 않으며 1차 연동은 Basic Auth 또는 사내 Reverse Proxy 인증을 지원합니다. 비밀번호는 인스턴스 키로 암호화되고 브라우저에 다시 노출되지 않습니다.

Background Worker는 CQL `lastmodified` 조건과 Pagination으로 마지막 성공 시각 이후의 Page를 증분 수집합니다. 본인이 이번 주 생성한 Page와 본인이 마지막으로 수정한 Page를 사용자 활동으로 판정하며, 제목·Space·작성자·수정자·버전을 먼저 저장합니다. 규칙 점수를 통과한 Page는 제한된 `body.storage` 미리보기를 함께 사용해 제목이 일반적인 문서도 실제 내용으로 업무 여부를 판정하고 유사 문서를 병합합니다. 본문 XHTML은 제목·문단·목록·표 행과 셀 경계를 보존하고 입력 상한에서는 앞·뒤 문맥을 남깁니다. 요약은 실적·계획·이슈의 독립 사실 단위와 각 사실을 뒷받침하는 `evidencePageIds`로 구조화하며, 서버는 근거 ID가 실제 AI 입력 Page 집합에 속하는지 검증합니다. 잘못된 근거에는 실패 사유를 포함해 한 번 교정을 요청하고 재실패하면 결정적 제목 기반 후보로 전환합니다. 검색 메타데이터 Version과 본문 조회 Version이 다르면 동시 수정으로 판단해 해당 본문을 이번 회차에서 제외하고 다음 증분 Sync에서 다시 처리합니다. 원문은 DB나 애플리케이션 로그에 보존하지 않습니다. AI 분류가 일부 Page를 누락한 경우에도 불완전한 AI 결과를 그대로 저장하지 않고 결정적 폴백으로 전환합니다.

사용자 식별자는 다음 순서로 유일하게 매핑합니다.

1. 관리자가 명시한 Confluence 아이디
2. Keycloak/Weekly 이메일의 `@` 앞부분
3. Weekly 로그인 아이디

예를 들어 Keycloak 이메일 `hkjang@koreacb.com`은 Confluence 사용자 `hkjang`과 자동 연결됩니다. 후보 화면에서는 출처 Page를 확인하고 제목·실적·계획·이슈를 수정하거나 제외할 수 있습니다. 사용자 수정본은 재동기화로 덮어쓰지 않으며, 제외한 같은 Page는 같은 주차에 다시 생성되지 않습니다. 보고서에 반영해 저장한 항목은 `CONFLUENCE_AI` 출처가 기록됩니다.

구체적인 설정값, 동기화·장애 처리와 데이터 모델은 [Confluence 연동 문서](docs/CONFLUENCE.md)를 참고하십시오.

## API와 MCP

REST API 기본 경로는 `/api/v1`이며 응답 규격은 다음과 같습니다.

```json
{"success":true,"data":{},"traceId":"9f2c8d11a83d6720"}
```

개인 설정에서 발급한 `wky_...` 키를 MCP 클라이언트의 Bearer 토큰으로 사용합니다.

```text
URL: https://weekly.example.internal/mcp
Transport: Streamable HTTP
Authorization: Bearer wky_...
```

제공 도구:

- `weekly_submission_overview`: 주차별 제출률, 상태, 이슈, 진척도 분석
- `weekly_reports_search`: 호출자 권한 범위의 보고서 검색
- `weekly_endpoint_analysis`: 최근 24시간 API 호출·지연·오류 분석, 관리자 전용

세부 규격은 [MCP 문서](docs/MCP.md)와 [OpenAPI 초안](docs/openapi.yaml)을 참고하십시오.

## 개발

요구 버전은 Go 1.24+, Node.js 24+, PostgreSQL 15+입니다.

```bash
cd frontend && npm ci && npm run build
cd .. && go test ./...
./scripts/build.sh
```

로컬 실행에도 운영과 동일한 세 필수 환경변수가 필요합니다. 운영과 같은 비밀 설정 복구를 검증할 때는 `WEEKLY_ENCRYPTION_KEY`도 지정합니다. API는 `:8080`에 고정되어 있으며 프록시/Ingress에서 TLS를 종료하는 구성을 권장합니다.

## 릴리즈

`v*` 태그를 푸시하면 GitHub Actions가 `weekly:<tag>` 형식(예: `weekly:v0.7.0`)의 `linux/amd64` 서비스 이미지를 빌드하고 `weekly-<tag>.tar.gz` 형식의 단일 자산만 GitHub Release에 올립니다. 릴리즈 본문은 `.github/release-notes/<tag>.md`에 기능, 설정, 업그레이드, 보안, 검증, 알려진 제약을 작성해야 하며 파일이 없거나 비어 있으면 배포가 실패합니다. 워크플로가 실제 자산명·크기·SHA-256을 본문 끝에 자동 추가합니다.

```bash
git tag v0.7.0
git push origin v0.7.0
```

로컬에서는 `make offline`으로 같은 형식의 파일을 만들 수 있습니다.

## 설계 문서

- [요구사항 및 시스템 설계](docs/DESIGN.md)
- [보안 및 운영](docs/OPERATIONS.md)
- [MCP 연동](docs/MCP.md)
- [Confluence Server 6.9.1 자동화](docs/CONFLUENCE.md)
- [OpenAPI](docs/openapi.yaml)
