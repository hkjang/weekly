import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError } from './api'
import ManualTeamReminder, {
  MANUAL_TEAM_REMINDER_CONFIRMATION,
  ManualTeamReminderResult,
  claimManualTeamReminderRun,
  confirmManualTeamReminder,
  manualTeamReminderButtonState,
  manualTeamReminderErrorMessage,
  manualTeamReminderResultSummary,
  queueManualTeamReminder,
} from './ManualTeamReminder'
import type { TeamReminderQueueResult } from './types'

const result: TeamReminderQueueResult = {
  weekStart: '2026-08-31', eligible: 8, queued: 3, alreadyQueued: 4, skippedNoAddress: 1,
}

afterEach(() => vi.unstubAllGlobals())

describe('팀원 작성 권고 수동 발송', () => {
  it('권한이 없으면 수동 발송 구획을 만들지 않는다', () => {
    expect(renderToStaticMarkup(<ManualTeamReminder available={false} relayReady notify={() => undefined}/>)).toBe('')
  })

  it('자동 설정과 독립임을 설명하고 SMTP 미준비 시 버튼을 막는다', () => {
    const markup = renderToStaticMarkup(<ManualTeamReminder available relayReady={false} notify={() => undefined}/>)
    expect(markup).toContain('<h3 id="manual-team-reminder-heading">수동 발송</h3>')
    expect(markup).toContain('aria-labelledby="manual-team-reminder-heading"')
    expect(markup).toContain('자동 발송 사용 여부·요일과 독립적으로')
    expect(markup).toContain('별도 저장도 필요하지 않습니다.')
    expect(markup).toContain('메일 서버를 준비하기 전에는 지금 발송할 수 없습니다.')
    expect(markup).toContain('disabled=""')
  })

  it('동기 실행 잠금은 해제 전 같은 렌더 틱의 두 번째 요청을 거부한다', () => {
    const guard = { current: false }
    expect(claimManualTeamReminderRun(guard)).toBe(true)
    expect(claimManualTeamReminderRun(guard)).toBe(false)
    guard.current = false
    expect(claimManualTeamReminderRun(guard)).toBe(true)
  })

  it('확인 문구가 중복 방지와 자동 설정 독립성을 먼저 알린다', () => {
    expect(MANUAL_TEAM_REMINDER_CONFIRMATION).toContain('자동 발송 설정과 관계없이')
    expect(MANUAL_TEAM_REMINDER_CONFIRMATION).toContain('중복으로 추가하지 않습니다')
    const cancelled = vi.fn(() => false)
    expect(confirmManualTeamReminder(cancelled)).toBe(false)
    expect(cancelled).toHaveBeenCalledOnce()
  })

  it('SMTP 상태와 처리 중 상태 모두에서 재요청을 막고 진행 문구를 쓴다', () => {
    expect(manualTeamReminderButtonState(false, false)).toEqual({
      disabled: true, label: '지금 권고 메일 보내기',
    })
    expect(manualTeamReminderButtonState(true, true)).toEqual({
      disabled: true, label: '대상 확인·큐 추가 중…',
    })
    expect(manualTeamReminderButtonState(true, false).disabled).toBe(false)
  })

  it('확인 뒤 정해진 API에 빈 JSON 본문으로 POST한다', async () => {
    const calls: { url: string; init: RequestInit }[] = []
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
      calls.push({ url, init })
      return {
        ok: true,
        status: 202,
        json: async () => ({ success: true, data: result, traceId: 'trace-reminder' }),
      }
    })
    await expect(queueManualTeamReminder()).resolves.toEqual(result)
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('/api/v1/me/team-reminders')
    expect(calls[0].init.method).toBe('POST')
    expect(calls[0].init.body).toBe('{}')
  })
})

describe('팀원 작성 권고 처리 결과', () => {
  it('대상·큐잉·중복·주소 없음 수를 모두 지속 영역에 표시한다', () => {
    const markup = renderToStaticMarkup(<ManualTeamReminderResult result={result}/>)
    expect(markup).toContain('2026-08-31 주차 처리 결과')
    for (const text of ['권고 대상', '8명', '새로 발송 대기', '3명', '중복 제외', '4명', '주소 없음', '1명']) {
      expect(markup).toContain(text)
    }
    expect(markup).not.toContain('role="status"')
    expect(markup).not.toContain('aria-live')
    expect(markup).toContain('warning')
  })

  it('부분 성공도 중복과 주소 누락 사유를 함께 요약한다', () => {
    const summary = manualTeamReminderResultSummary(result)
    expect(summary).toContain('3명의 권고 메일을 발송 대기열에 추가했습니다.')
    expect(summary).toContain('4명은 이번 주에 이미 발송했거나 대기 중')
    expect(summary).toContain('1명은 유효한 메일 주소가 없어')
  })

  it('대상이 없을 때와 주소가 없어 한 명도 큐잉하지 못했을 때를 구분한다', () => {
    const empty = { ...result, eligible: 0, queued: 0, alreadyQueued: 0, skippedNoAddress: 0 }
    expect(manualTeamReminderResultSummary(empty)).toBe('현재 권고할 미제출 팀원이 없습니다.')

    const undeliverable = { ...empty, eligible: 2, skippedNoAddress: 2 }
    expect(manualTeamReminderResultSummary(undeliverable)).toContain('새로 발송 대기열에 추가된 메일이 없습니다.')
    expect(manualTeamReminderResultSummary(undeliverable)).toContain('2명은 유효한 메일 주소가 없어')
  })

  it('SMTP와 역할 오류는 상태가 오래된 화면에서도 구체적으로 안내한다', () => {
    expect(manualTeamReminderErrorMessage(
      new APIError(409, 'MAIL_RELAY_NOT_READY', 'conflict'),
    )).toContain('메일 서버를 준비하지 않아')
    expect(manualTeamReminderErrorMessage(
      new APIError(403, 'REMINDER_ROLE_REQUIRED', 'forbidden'),
    )).toContain('팀장 이상의 권한')
  })
})
