# Weekly 엔터프라이즈 사용자 가이드 (User Guide & Employee Manual)

- **문서 버전**: v0.1.0-ENTERPRISE  
- **대상**: 임직원 작성자, 팀장(검토/승인자), 조직 관리자, AI MCP 클라이언트 사용자  
- **문서 개요**: 주간업무보고 작성 방법, 승인 신청 및 반려 대응, PPTX 원클릭 내보내기, API 및 Streamable MCP 활용 매뉴얼  

---

## 1. 개요

Weekly 서비스는 개인 업무 작성부터 팀 승인, PPTX 자동 내보내기까지 단일 화면에서 직관적으로 사용할 수 있는 주간보고 시스템입니다.

---

## 2. 주간업무보고 작성 및 제출 (Employee Workflow)

### 2.1 작성 화면 구성
- **주차 선택**: 상단 주차 선택 드롭다운에서 작성 타겟 주차(예: `2026년 32주차`) 선택.
- **ReportItem 항목 추가**:
  - `금주 실적 (This Week)`: 금주 완료된 주요 실적 항목 작성
  - `차주 계획 (Next Week)`: 차주 진행 예정 업무 및 달성 목표 작성
  - `이슈 및 지원요청 (Issues)`: 리스크 항목 및 타 부서 지원 필요사항 작성
  - `진척도 (%)`: 업무 완료 비율 입력

### 2.2 승인 제출 신청 (`Draft` ➔ `Submitted`)
1. 내용 작성이 완료되면 하단의 **[승인 요청 / 제출]** 버튼 클릭.
2. 상태가 `SUBMITTED`로 전환되며 본인 수정이 잠금 처리되고, 담당 팀장에게 검토 알림이 전달됩니다.

---

## 3. 팀장 승인 및 검토 매뉴얼 (Team Leader Workflow)

관리자 설정에서 `workflow.enabled = true` 상태일 때 검토 및 승인 절차가 활성화됩니다.

```
 [DRAFT (초안)]  ➔ (임직원 제출) ➔  [SUBMITTED (제출 완료)]
                                           │
                           ┌───────────────┴───────────────┐
                           ▼                               ▼
                 [APPROVED (승인 완료)]     [REVISION_REQUESTED (보완 요청)]
```

- **승인 (Approve)**: 보고 내용 확인 후 [승인] 버튼을 클릭하면 상태가 `APPROVED`로 확정됩니다.
- **반려/보완 요청 (Revision Request)**: 보완이 필요한 경우 사유를 작성하여 [수정 요청]을 클릭하면 작성자에게 알림이 전달되며 `REVISION_REQUESTED` 상태로 되돌아갑니다.

---

## 4. PPTX 원클릭 내보내기 (PPTX Export)

1. 보고서 상세 페이지 또는 팀 종합 보고서 화면 상단의 **[PPTX 다운로드]** 버튼 클릭.
2. 시스템이 관리자에 의해 등록된 원본 PPTX 템플릿의 슬라이드 마스터, 레이아웃, 도형, 폰트를 그대로 유지한 채 `{{THIS_WEEK}}`, `{{NEXT_WEEK}}`, `{{ISSUES}}` 토큰 위치에 실시간 치환 결과를 파일로 다운로드합니다.

---

## 5. API / MCP Key 발급 및 AI 에이전트 연동

1. 우측 상단 프로필 메뉴 ➔ **[개인 API/MCP 키 관리]**로 이동.
2. **[신규 MCP 키 발급]**을 클릭하여 `wky_...` 형태의 키 생성.
3. Claude Desktop 또는 Cursor의 MCP 설정파일(`claude_desktop_config.json`)에 아래와 같이 등록:

```json
{
  "mcpServers": {
    "weekly": {
      "command": "curl",
      "args": [
        "-X", "POST",
        "-H", "Authorization: Bearer wky_58a1f90b2c3d4e5f",
        "https://weekly.internal/mcp"
      ]
    }
  }
}
```
