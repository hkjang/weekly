# Weekly 엔터프라이즈 관리자 가이드 (Admin & Operational Guide)

- **문서 버전**: v0.1.0-ENTERPRISE  
- **대상**: 시스템 관리자, Security/DevOps 엔지니어, 데이터 보안 담당자  
- **문서 개요**: Weekly 단일 컨테이너 환경변수 부트스트랩, Keycloak OIDC SSO 연동, RBAC 권한 매핑, PPTX 템플릿 등록 및 감사 로그 운영  

---

## 1. 시스템 부트스트랩 (Bootstrap Environment Variables)

Weekly 컨테이너 프로세스는 오직 **3개의 필수 환경변수**만으로 최소 인프라 구동을 완료합니다.

```bash
# deploy/.env 파일 설정 예시
WEEKLY_POSTGRES_DSN=postgres://weekly:Secr3tPass@10.10.30.5:5432/weekly?sslmode=disable
WEEKLY_BOOTSTRAP_ADMIN=admin
WEEKLY_BOOTSTRAP_ADMIN_PASSWORD=SuperSecretAdminPassword123!
```

> **중요 (Volume Backup Notice)**:  
> `/var/lib/weekly` 볼륨은 반드시 정기 백업해야 합니다. 해당 볼륨에는 Keycloak Client Secret 암호화 키와 사용자가 등록한 PPTX 템플릿 파일이 저장되며, 분실 시 암호화된 설정 복호화가 불가능합니다.

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

---

## 4. API / MCP 키 무중단 회전 (Key Rotation) & 감사 로그 (Audit Log)

- **키 회전**: 관리자는 사내 보안 위협 시 [전체 개인 키 즉시 폐기] 기능을 실행하여 발급된 모든 API/MCP 키를 일괄 무효화할 수 있습니다.
- **감사 로그 (Audit Trail)**: 보고서 작성, 승인, 반려, OIDC 설정 변경 및 템플릿 업로드 내역이 DB 감사 로그 테이블에 영구 보존됩니다.
