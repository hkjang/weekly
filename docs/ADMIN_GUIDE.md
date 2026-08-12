# Weekly 엔터프라이즈 관리자 가이드 (Admin & Operational Guide)

- **문서 버전**: v0.7.0-ENTERPRISE
- **대상**: 시스템 관리자, Security/DevOps 엔지니어, 데이터 보안 담당자  
- **문서 개요**: Weekly 단일 컨테이너 환경변수 부트스트랩, Keycloak OIDC SSO 연동, RBAC 권한 매핑, PPTX 템플릿 등록 및 감사 로그 운영  

---

## 1. 시스템 부트스트랩 (Bootstrap Environment Variables)

Weekly 컨테이너 프로세스는 **3개의 필수 환경변수**로 부팅하며, 업그레이드 후에도 암호화 설정을 복구하기 위해 `WEEKLY_ENCRYPTION_KEY`를 운영 필수 수준으로 권장합니다.

```bash
# deploy/.env 파일 설정 예시
WEEKLY_POSTGRES_DSN=postgres://weekly:Secr3tPass@10.10.30.5:5432/weekly?sslmode=disable
WEEKLY_BOOTSTRAP_ADMIN=admin
WEEKLY_BOOTSTRAP_ADMIN_PASSWORD=SuperSecretAdminPassword123!
WEEKLY_ENCRYPTION_KEY=<openssl rand -base64 32 결과>
```

> **중요 (Volume Backup Notice)**:  
> `/var/lib/weekly` 볼륨과 `WEEKLY_ENCRYPTION_KEY`는 별도로 정기 백업해야 합니다. 기존 볼륨 키만 사용하던 환경은 볼륨을 유지한 상태에서 환경 키를 처음 설정하면 Keycloak/AI/Confluence 비밀값이 새 키로 자동 재암호화됩니다.

---

## 2. Keycloak OIDC SSO 및 RBAC 연동

1. 부트스트랩 관리자 계정으로 로그인 ➔ `관리자 설정 ➔ 서비스 설정 ➔ 인증 · Keycloak OIDC`로 이동.
2. Valid Redirect URI 등록: `https://weekly.internal/api/v1/auth/oidc/callback`
3. Group Claim Mappers 설정 및 관리자 그룹(`ADMIN`) 지정 시 자동 승격 연동.

### RBAC (Role-Based Access Control) 권한 매트릭스

| 역할 (Role) | 개인 보고서 작성 | 팀 승인/반려 | 조직 전체 조회 | PPTX 템플릿 등록 | OIDC/시스템 설정 | 감사 로그 |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| **ADMIN** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **ORG_MANAGER** | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |
| **TEAM_LEADER** | ✅ | ✅ | ❌ (소속 팀만) | ❌ | ❌ | ❌ |
| **USER** | ✅ | ❌ | ❌ (본인 것만) | ❌ | ❌ | ❌ |

---

## 3. 사내 PPTX 템플릿 등록 및 토큰 검증

`관리자 설정 ➔ PPTX 템플릿` 메뉴에서 사내 표준 PPTX 파일을 등록합니다. 등록 시 ZIP 구조와 아래 필수 토큰 포함 여부를 자동으로 시스템이 검증합니다.

- `{{WEEK_SCHEDULE}}`: 주간 일정 범위
- `{{THIS_WEEK}}`: 업무별 이번 주 한 일 (필수)
- `{{NEXT_WEEK}}`: 업무별 다음 주 할 일 (필수)
- `{{ISSUES}}`: 주요 이슈 및 지원 요청
- `{{AUTHOR}}`: 작성자
- `{{TEAM}}`: 조직명

기본 4장 참조 형식은 항목 순서를 유지하면서 구분·제목, 예상 줄바꿈과 실적·계획 중 더 긴 열을 비용으로 계산해 슬라이드 높이를 균형화합니다. 같은 구분이 한 장을 넘으면 여러 장으로 나누고 각 장에서 구분 제목을 반복합니다. 관리자 등록 토큰 템플릿은 기존처럼 원본 슬라이드 수와 레이아웃을 변경하지 않습니다.

---

## 4. API / MCP 키 무중단 회전 (Key Rotation) & 감사 로그 (Audit Log)

- **키 회전**: 관리자는 사내 보안 위협 시 [전체 개인 키 즉시 폐기] 기능을 실행하여 발급된 모든 API/MCP 키를 일괄 무효화할 수 있습니다.
- **감사 로그 (Audit Trail)**: 보고서 작성, 승인, 반려, OIDC 설정 변경 및 템플릿 업로드 내역이 DB 감사 로그 테이블에 영구 보존됩니다.

---

## 5. AI Gateway 및 Import 정책

`서비스 설정`의 주차 시작 요일은 월·화·수·목·금·토·일 중 하나를 선택할 수 있습니다. 이 값은 현재 주차 조회, 팀 분석, Confluence 후보, PPTX 날짜와 Import의 단독 날짜 보정에 공통 적용됩니다.

`관리자 설정 ➔ AI · Import`에서 다음 연결값을 관리합니다. 배포 환경에는 연결값 대신 공통 암호화 키만 둡니다.

| 설정 | 의미 | 기본값 |
|---|---|---:|
| AI 사용 | 사용자 AI 작성·PPTX 분석 허용 | 꺼짐 |
| Endpoint | OpenAI 호환 Chat Completions 전체 URL | 없음 |
| Model | Gateway가 제공하는 모델 식별자 | 없음 |
| API Key | 선택형 Bearer 토큰, 암호화 저장 | 없음 |
| Timeout | 한 번의 AI 호출 제한 시간(초) | 90 |
| 최대 입력 문자 | 정규화 입력 상한 | 50,000 |
| 작업당 파일 수 | 다중 업로드 상한 | 20 |
| 파일당 크기 | PPTX 한 개 상한(MB) | 25 |
| 원본 보존일 | 확정·건너뜀·실패 원본 보존기간 | 365 |

Endpoint와 Model을 먼저 저장하고 `AI 연결 시험`으로 JSON Schema Structured Output 지원 여부를 확인한 다음 AI 사용을 켭니다. v0.7.0의 모델 계약은 실적·계획·이슈를 원자 문자열 배열로 반환하며, PPTX 분석은 각 항목에 실제 근거 슬라이드 번호와 별도 업무 구분 신뢰도를 요구합니다. 존재하지 않는 슬라이드, 근거 없는 항목 또는 잘못된 구조를 반환하는 모델은 분석 실패로 처리되므로 업그레이드 전에 운영 모델의 Structured Output 호환성을 다시 시험하십시오. API Key 입력란을 비워 저장하면 기존 비밀값이 유지됩니다. 내부 Gateway가 인증을 요구하지 않는 경우 API Key는 비워둘 수 있습니다.

원본 보존기간이 지나면 해당 PPTX 바이너리만 상태 볼륨에서 삭제됩니다. 파일 해시, 추출 텍스트, 구조화 결과, 연결된 보고서와 감사 기록은 PostgreSQL에 남습니다. 원본이 남아 있고 아직 확정되지 않은 `FAILED`, `READY`, `NEEDS_REVIEW` 파일은 파일 ID를 지정해 다시 분석할 수 있습니다. 본문 없는 기존 호출은 종전처럼 실패 파일 전체만 재시도하므로 기존 클라이언트와 호환됩니다. `NEEDS_REVIEW`는 UI에서 기본 선택하지 않고, 확정 API도 관리자 주차 시작 요일과 항목 구분·제목을 다시 검증합니다.

보안상 AI에는 PPTX 파일 자체가 아니라 서버에서 추출·정규화한 텍스트만 전달됩니다. 추출기는 프레젠테이션 관계의 화면 표시 순서, 원본 슬라이드 번호, 도형 이름, 문단·글머리표 단계, 표 행·열과 빈 셀을 보존합니다. 입력 상한을 넘으면 슬라이드별 공정 예산으로 자르고 일부 내용이 잘린 슬라이드를 `NEEDS_REVIEW` 경고에 기록하므로 운영자는 해당 원본을 대조해야 합니다. 데이터 반출 정책상 외부 AI 호출이 허용되지 않는 환경에서는 사내망 OpenAI 호환 Gateway를 사용하십시오.

## 6. Confluence 6.9.1 자동화

`서비스 설정 ➔ Confluence 6.9.1 자동화`에서 Base URL, `BASIC` 인증, 연동 전용 계정과 암호화 비밀번호를 저장한 뒤 연결 시험을 수행합니다. 대상·제외 Space, 5분 이상의 동기화 주기, Blog 포함 여부, AI/본문 분석, 후보 점수와 업무 키워드를 운영 환경에 맞게 지정합니다. Confluence 연결값 자체는 환경변수로 전달하지 않습니다.

`Confluence 자동화` 탭에서는 마지막 성공·시도 시각, 조회/변경 Page 수, 생성 후보, 실패 수와 최근 단계별 오류를 확인하고 관리자 강제 증분 Sync를 요청할 수 있습니다. 일반 사용자에게는 Sync 버튼을 제공하지 않습니다.

사용자 매핑 우선순위는 관리자 명시값, Keycloak/Weekly 이메일의 `@` 앞부분, Weekly 로그인 아이디입니다. 예를 들어 `hkjang@koreacb.com`은 Confluence `hkjang`에 자동 매핑됩니다. 유일하게 판정할 수 없는 미매핑 사용자만 표에서 직접 지정합니다.

Confluence 본문은 PostgreSQL이나 로그에 저장되지 않습니다. 규칙 점수를 통과한 Page의 제한된 본문 미리보기는 제목·문단·목록·표 구조와 입력 앞·뒤 문맥을 보존해 AI 업무 판정과 원자 사실 요약에 일시 사용됩니다. 모델은 각 사실에 하나 이상의 `evidencePageIds`를 반환하고, 서버는 비어 있거나 실제 입력 Page 집합에 없는 ID를 거부합니다. 의미 검증 실패 시 사유를 포함해 한 번만 교정 호출하며 재실패하면 `AI_SUMMARY` 진단 후 결정적 제목 기반 후보로 대체합니다. 검색 메타데이터와 본문 조회 사이에 Page Version이 달라지면 `BODY_VERSION_CHANGED` 단계 진단을 남기고 해당 버전을 후보에 연결하지 않습니다. 이는 동기화 중 수정된 두 버전의 내용을 섞지 않기 위한 재시도 가능한 상태입니다. 외부 AI 데이터 반출이 허용되지 않으면 사내 AI Gateway를 사용하거나 `후보 문서 본문 분석`을 끄십시오. 전체 운영 규격과 장애 코드는 [Confluence 연동 문서](CONFLUENCE.md)를 참고하십시오.
