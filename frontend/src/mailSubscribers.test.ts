import { describe, expect, it } from 'vitest'
import { mailSubscribersSummary } from './pages/AdminPage'

describe('관리자 메일 카드의 수신 현황', () => {
  it('종류별로 수신을 켠 사람 수를 활성 사용자 수와 함께 말한다', () => {
    expect(mailSubscribersSummary({ activeUsers: 300, excludedUsers: 0, report: 12, scheduleReminder: 5, teamReminder: 40 }))
      .toBe('수신을 켠 사람 — 제출 사본 12명 · 마감 알림 5명 · 작성 권고 40명 (활성 사용자 300명 중)')
  })

  it('메일 발송 제외 조직 소속이 있으면 그들은 켜도 받지 않는다고 덧붙인다', () => {
    expect(mailSubscribersSummary({ activeUsers: 10, excludedUsers: 3, report: 0, scheduleReminder: 0, teamReminder: 1 }))
      .toContain('메일 발송 제외 조직 소속 3명은 켜도 받지 않습니다.')
  })
})
