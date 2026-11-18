import { describe, expect, it } from 'vitest'
import { mailReceivingLabel } from './pages/ProfilePage'

describe('개인 설정 메일 카드의 상태 표시', () => {
  it('아무것도 켜지 않았으면 모두 꺼짐이다 — 처음 상태', () => {
    expect(mailReceivingLabel({ onSubmit: false, scheduleReminder: false, teamReminder: false })).toBe('모두 꺼짐')
  })

  it('제출 메일만이 아니라 세 가지 모두를 센다', () => {
    expect(mailReceivingLabel({ onSubmit: false, scheduleReminder: true, teamReminder: false })).toBe('받는 메일 1가지')
    expect(mailReceivingLabel({ onSubmit: false, scheduleReminder: false, teamReminder: true })).toBe('받는 메일 1가지')
    expect(mailReceivingLabel({ onSubmit: true, scheduleReminder: true, teamReminder: true })).toBe('받는 메일 3가지')
  })
})
