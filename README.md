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
- 개인 화면과 관리자 관리 화면의 명확한 분리
- 관리자 설정에 따라 켜지는 팀장 검토·승인·반려 흐름
- Keycloak 호환 OIDC Discovery, Authorization Code + PKCE, nonce 검증
- 조직 계층 기반 RBAC: `USER`, `TEAM_LEADER`, `ORG_MANAGER`, `ADMIN`
- Argon2id 로컬 인증, DB 세션, 감사 로그, 낙관적 잠금
- 개인 API/MCP 키 발급·폐기 및 전체 즉시 폐기형 개인 키 회전
- 제출률, 이슈, 진척도와 최근 24시간 API 성능 분석
- REST API와 읽기 전용 Streamable HTTP MCP 서버
- 관리자가 등록한 원본 PPTX 규격을 보존하는 주간보고 내보내기
- 로그인 화면과 프로필 컨텍스트 메뉴의 빌드 버전 표시
- 프론트엔드 정적 자산을 포함한 단일 오프라인 Docker 이미지

## 오프라인 설치

GitHub Release에서 `weekly-v<VERSION>-linux-amd64-docker.tar.gz` 하나만 반입합니다.

```bash
gzip -dc weekly-v0.1.0-linux-amd64-docker.tar.gz | docker load
cp deploy/.env.example deploy/.env
# deploy/.env의 세 값을 운영 환경에 맞게 변경
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d
```

Weekly가 받는 환경변수는 아래 세 개뿐입니다.

| 환경변수 | 설명 |
|---|---|
| `WEEKLY_POSTGRES_DSN` | PostgreSQL 연결 문자열 |
| `WEEKLY_BOOTSTRAP_ADMIN` | 최초 관리자 아이디 |
| `WEEKLY_BOOTSTRAP_ADMIN_PASSWORD` | 최초 관리자 비밀번호, 12자 이상 |

최초 관리자가 만들어진 뒤에는 환경변수의 비밀번호를 바꿔도 기존 관리자 비밀번호를 덮어쓰지 않습니다. 서비스 이름, 공지, 워크플로, OIDC, 세션, 키 유효기간, 분석 보존기간과 PPTX 템플릿은 모두 관리자 화면에서 관리합니다.

> `/var/lib/weekly` 볼륨은 반드시 백업하십시오. OIDC Client Secret 암호화용 인스턴스 키와 사용자 PPTX 템플릿이 저장됩니다. 이 볼륨을 잃으면 암호화된 설정을 복호화할 수 없습니다.

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

모든 상태 전이는 `report_status_history`와 감사 로그에 기록됩니다.

## PPTX 내보내기

`1월5주간업무보고_AI엔지니어링.pptx`와 같은 4장 구성, 슬라이드 크기, 조직명, 추진실적·추진계획 표와 글꼴 규격의 기본 템플릿을 코드에서 생성합니다. 원본 파일이 빌드 컨텍스트에 있으면 원본 슬라이드의 마스터, 표, 도형, 글꼴과 이미지를 우선 그대로 보존합니다. 두 방식 모두 조직명, 추진실적·추진계획 일정과 이번 주/다음 주 업무만 자동으로 교체하며, 업무 항목은 `category`별로 4개 슬라이드에 배치됩니다. 따라서 저작물인 원본 PPTX 바이너리를 Git이나 오프라인 이미지에 별도로 포함하지 않아도 기본 내보내기가 동작합니다.

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

로컬 실행에도 운영과 동일한 세 환경변수가 필요합니다. API는 `:8080`에 고정되어 있으며 프록시/Ingress에서 TLS를 종료하는 구성을 권장합니다.

## 릴리즈

`v*` 태그를 푸시하면 GitHub Actions가 `linux/amd64` 서비스 이미지를 빌드하고 `docker save | gzip` 형식의 단일 `.tar.gz` 자산만 GitHub Release에 올립니다.

```bash
git tag v0.1.0
git push origin v0.1.0
```

로컬에서는 `make offline`으로 같은 형식의 파일을 만들 수 있습니다.

## 설계 문서

- [요구사항 및 시스템 설계](docs/DESIGN.md)
- [보안 및 운영](docs/OPERATIONS.md)
- [MCP 연동](docs/MCP.md)
- [OpenAPI](docs/openapi.yaml)
