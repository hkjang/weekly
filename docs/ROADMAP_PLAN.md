# Weekly 엔터프라이즈 중장기 기술 로드맵 (Product Roadmap Plan)

- **문서 버전**: v0.3.0 ~ v2.0-VISION
- **작성일자**: 2026년 8월 9일
- **문서 분류**: 비즈니스 및 아키텍처 중장기 로드맵 (Strategic Product Roadmap)  

---

## 1. 비전 및 발전 마일스톤 개요

Weekly 플랫폼은 오프라인망 기반 수집 및 PPTX 자동화를 시작으로, 사내 AI 데이터 에이전트와 자연어 대화형으로 주간보고를 작성·분석하는 차세대 Enterprise Reporting Platform으로 발전합니다.

```
========================================================================================
                          [Weekly 단계별 마일스톤 아키텍처]
========================================================================================
 [Phase 1: v0.1.0] (완료) ➔ Single Container, PPTX Zip Engine, Keycloak OIDC, MCP 1.0
 [Phase 1.5: v0.2.0] (완료) ➔ AI Text Draft, Historical PPTX Import, Structured Output
 [Phase 1.75: v0.3.0] (완료) ➔ Confluence 6.9.1 Incremental Activity Draft Automation
 [Phase 2: v0.5.0] (진행) ➔ Multi-PPTX Template Mapping, Team Analytics & Slack/Teams Alert
 [Phase 3: v1.0.0] (2026 Q4) ➔ Source-system Agent Automation & MCP 2.0
 [Phase 4: v2.0.0] (2027)    ➔ Enterprise Autonomous Progress Copilot & Executive BI
========================================================================================
```

---

## 2. Phase별 세부 기술 명세

### 2.1 Phase 1: v0.1.0 오프라인 수집 및 PPTX 엔진 구축 (완료)
- **ReportItem 모델**: 금주 실적, 차주 계획, 주요 이슈, 진척도 입력 구조화.
- **PPTX 템플릿 엔진**: 슬라이드 디자인, 마스터, 글꼴 100% 보존 토큰 자동 치환.
- **Keycloak OIDC & RBAC**: PKCE 지원 및 4단계 조직 계층 RBAC 권한 분리.
- **Analytics MCP 1.0**: Streamable HTTP MCP 서버로 submission_overview, reports_search 도구 탑재.

### 2.2 Phase 1.5: v0.2.0 AI 구조화 및 과거 자료 전환 (완료)

- **자유 텍스트 AI 초안**: 붙여 넣은 메모를 Structured Output으로 변환하고 사용자 검토 후 적용.
- **과거 PPTX Import**: Open XML 파싱, 주차 감지, SHA-256 중복 확인, 비동기 분석 및 병합·교체 확정.
- **사내 AI Gateway**: 관리자 UI에서 OpenAI 호환 Endpoint, 모델, 암호화 API Key와 운영 제한 관리.

### 2.3 Phase 1.75: v0.3.0 Confluence 활동 자동 초안 (완료)

- **증분 Source 수집**: Confluence 6.9.1 CQL, Pagination, Version 중복 방지와 백그라운드 Worker.
- **Identity Mapping**: 명시 매핑, Keycloak 이메일 로컬파트, Weekly 아이디 순서의 안전한 자동 연결.
- **근거 기반 Candidate**: Rule Score, AI 제목 Cluster, 후보 본문만 요약하고 N:M 원본 Page 출처 유지.
- **사용자 통제**: 수정 보호, 제외 후 미재생성, 검토·보고서 반영과 `CONFLUENCE_AI` 출처 추적.

### 2.4 Phase 2: v0.5.0 고도화 & 팀 단위 분석 알림 (2026 Q3)
- **다중 PPTX 템플릿**: 사업부별/부서별 서로 다른 PPTX 양식 동적 선택 지정.
- **미제출자 자동 알림**: 금요일 오후 미제출 임직원 대상 Slack/Teams/사내 메일 자동 알림.

### 2.5 Phase 3: v1.0.0 소스 시스템 기반 AI 에이전트 자동화 (2026 Q4)

- **Source-to-Report Agent (MCP 2.0)**: 사용자 동의하에 커밋·결재·Jira 같은 시스템에서 근거를 수집해 `ReportItem` 후보와 출처 링크를 생성.

---

## 3. 리소스 및 품질 관리 전략

- **100% 테스트 자동화**: Go 백엔드 유닛 테스트 및 React UI 빌드 검증 자동화.
- **무중단 마이그레이션**: PostgreSQL Migration 자동 스키마 적용 및 백워드 호환성 보장.
