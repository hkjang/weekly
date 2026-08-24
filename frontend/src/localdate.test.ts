import { describe, expect, it } from 'vitest'
import { localDate, shiftWeeks, todayLocal } from './localdate'

// These three lines exist because four screens were quietly a day off. The bug
// was fixed in v0.65 and nothing has watched it since — and it is the kind that
// comes back the moment somebody reaches for toISOString() again, because that
// call looks like the obvious way to turn a Date into yyyy-mm-dd.
describe('localDate', () => {
  it('브라우저의 달력 날짜를 그대로 쓴다', () => {
    // A local midnight east of Greenwich is the previous day in UTC. Reading
    // the fields the browser already has avoids the conversion entirely.
    expect(localDate(new Date(2026, 7, 24, 0, 0, 0))).toBe('2026-08-24')
    expect(localDate(new Date(2026, 7, 24, 23, 59, 59))).toBe('2026-08-24')
  })

  it('한 자리 월과 일에 0을 채운다', () => {
    expect(localDate(new Date(2026, 0, 5))).toBe('2026-01-05')
    expect(localDate(new Date(2026, 8, 9))).toBe('2026-09-09')
  })

  it('연말과 윤년 경계에서 하루도 밀리지 않는다', () => {
    expect(localDate(new Date(2026, 11, 31, 23, 0, 0))).toBe('2026-12-31')
    expect(localDate(new Date(2027, 0, 1, 0, 30, 0))).toBe('2027-01-01')
    expect(localDate(new Date(2028, 1, 29))).toBe('2028-02-29')
  })

  it('UTC 변환을 쓰지 않는다', () => {
    // The regression this file exists to prevent, stated as a comparison: on a
    // machine east of Greenwich these two disagree, and the wrong one is the
    // one that reads naturally.
    const localMidnight = new Date(2026, 7, 24, 0, 0, 0)
    const viaUTC = localMidnight.toISOString().slice(0, 10)
    if (localMidnight.getTimezoneOffset() < 0) {
      expect(localDate(localMidnight)).not.toBe(viaUTC)
    }
    expect(localDate(localMidnight)).toBe('2026-08-24')
  })
})

describe('shiftWeeks', () => {
  it('같은 요일에 머문 채 주 단위로 움직인다', () => {
    // The meeting agenda's arrows. 2026-08-24 is a Monday; stepping forward has
    // to land on the next Monday, not on the seventh day after it.
    expect(shiftWeeks('2026-08-24', 1)).toBe('2026-08-31')
    expect(shiftWeeks('2026-08-24', -1)).toBe('2026-08-17')
    expect(shiftWeeks('2026-08-24', 0)).toBe('2026-08-24')
  })

  it('월과 해를 넘어도 요일을 지킨다', () => {
    expect(shiftWeeks('2026-12-28', 1)).toBe('2027-01-04')
    expect(shiftWeeks('2027-01-04', -1)).toBe('2026-12-28')
    expect(shiftWeeks('2026-08-24', 10)).toBe('2026-11-02')
  })

  it('결과가 언제나 같은 요일이다', () => {
    const start = new Date('2026-08-24T00:00:00')
    for (const weeks of [-8, -3, -1, 1, 4, 26, 52]) {
      const moved = new Date(`${shiftWeeks('2026-08-24', weeks)}T00:00:00`)
      expect(moved.getDay()).toBe(start.getDay())
    }
  })
})

describe('todayLocal', () => {
  it('오늘을 그 사람의 달력으로 답한다', () => {
    expect(todayLocal()).toBe(localDate(new Date()))
    expect(todayLocal()).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
