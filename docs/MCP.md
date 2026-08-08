# Weekly Analytics MCP

Weekly는 별도 프로세스 없이 `/mcp`에서 인증된 읽기 전용 MCP 도구를 제공한다.

## 연결

1. Weekly의 개인 설정에서 API/MCP 키를 발급한다.
2. MCP 클라이언트의 Streamable HTTP URL을 `https://<host>/mcp`로 설정한다.
3. `Authorization: Bearer wky_...` 헤더를 설정한다.

세션 쿠키도 웹 화면에서는 동작하지만 자동화/MCP 클라이언트에는 개인 키를 사용한다. 키에는 `mcp:read` 범위가 필요하다.

## 프로토콜

- JSON-RPC 2.0
- Streamable HTTP POST
- 기본 협상 프로토콜 버전: `2025-11-25` (`2025-03-26`, `2025-06-18` 호환)
- 구현 메서드: `initialize`, `notifications/initialized`, `ping`, `tools/list`, `tools/call`

## 도구

### weekly_submission_overview

입력:

```json
{"weekStart":"2026-01-05"}
```

사용자 권한 범위의 활성 사용자 수, 제출 수/제출률, 상태별 수, 이슈 수, 평균 진척도를 반환한다.

### weekly_reports_search

입력:

```json
{"weekStart":"2026-01-05","status":"APPROVED"}
```

일반 사용자는 본인, 팀장/조직장은 동일·하위 조직, 관리자는 전체 보고서를 최대 100건 조회한다.

### weekly_endpoint_analysis

입력은 빈 객체다. 관리자만 호출할 수 있으며 최근 24시간 API별 호출 수, 평균/최대 처리시간, 서버 오류와 전체 오류율을 반환한다.

## 키 회전

개인 설정의 `모든 키 회전`은 사용자 `key_version`을 증가시키고 활성 키를 모두 폐기한다. 실행 중인 모든 MCP 연결은 다음 요청부터 인증에 실패하므로 새 키를 발급해 클라이언트에 반영해야 한다.
