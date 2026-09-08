import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import SchedulePage from './pages/SchedulePage'
import type { SessionInfo } from './types'

/**
 * 상황판이 읽히지 않았을 때 무엇도 그리지 않는지.
 *
 * 이 판은 사무실 벽에 걸립니다. 읽기가 실패한 자리에 빈 달력을 그리면 그것을
 * 지나가며 보는 사람에게는 "이 부서는 이번 달에 아무 계획이 없습니다" 로
 * 읽힙니다 — 서버에 닿지 못했다는 말과는 전혀 다른 문장이고, 벽에 걸린 것을
 * 의심할 이유도 없습니다.
 *
 * renderToStaticMarkup 은 effect 를 돌리지 않으므로 여기서 그려지는 것은
 * 정확히 "아직 아무것도 읽지 못한" 상황판입니다.
 */
const session: SessionInfo = {
  user: { id: 1, username: 'hq1', displayName: '홍길동', email: 'hq1@example.com', role: 'USER', keyVersion: 1 },
  workflowEnabled: false, aiEnabled: false, confluenceEnabled: false,
  currentWeekStart: '2026-09-06', serviceName: 'Weekly', notice: '',
  build: { version: 'v0.293.0', commit: 'abc', builtAt: '2026-09-09T00:00:00Z' },
}

describe('업무 상황판', () => {
  const markup = renderToStaticMarkup(<SchedulePage session={session} notify={() => undefined}/>)

  it('읽지 못한 달을 빈 달력으로 그리지 않는다', () => {
    expect(markup).not.toContain('month-cell')
    expect(markup).not.toContain('board-day')
  })

  it('읽지 못한 것을 "없습니다" 라고 말하지 않는다', () => {
    expect(markup).not.toContain('없습니다')
    expect(markup).not.toContain('일정 없음')
    // 기다리면 오는 것과 오지 않는 것도 다릅니다.
    expect(markup).not.toContain('불러오는 중')
  })

  it('그래도 화면은 서 있다 — 보기와 이동은 그대로 눌린다', () => {
    expect(markup).toContain('업무 상황판')
    expect(markup).toContain('일정 추가')
  })
})
