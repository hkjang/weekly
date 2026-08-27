import { describe, expect, it } from 'vitest'
import { conflictWarning, statusName, strategyDiscardsReview } from './importConflict'

// A real deployment lost an approval to this: report 364 was APPROVED, the
// screen offered 병합 by default, and confirming turned it into 확정 with the
// review stamp cleared. The screen's only word about it was "(APPROVED)".

describe('conflictWarning', () => {
  it('보고서가 없으면 아무 말도 하지 않습니다', () => {
    expect(conflictWarning(undefined, '', 'MERGE')).toBe('')
  })

  it('승인된 보고서를 병합하면 승인이 사라진다고 말합니다', () => {
    const text = conflictWarning(364, 'APPROVED', 'MERGE')
    expect(text).toContain('#364(승인)')
    expect(text).toContain('더합니다')
    expect(text).toContain('승인 기록은 사라집니다')
  })

  it('교체는 기존 항목을 지운다고 병합과 다르게 말합니다', () => {
    const replace = conflictWarning(364, 'APPROVED', 'REPLACE')
    expect(replace).toContain('모두 지우고')
    expect(replace).not.toBe(conflictWarning(364, 'APPROVED', 'MERGE'))
  })

  it('이미 확정인 보고서에는 잃을 기록이 없다고 보지 않습니다', () => {
    expect(conflictWarning(364, 'CLOSED', 'REPLACE')).not.toContain('사라집니다')
  })

  it('건너뛰기는 그대로 둔다고 말합니다', () => {
    expect(conflictWarning(364, 'APPROVED', 'SKIP')).toContain('그대로 둡니다')
  })

  it('신규 생성은 저장되지 않는다고 미리 말합니다', () => {
    expect(conflictWarning(364, 'APPROVED', 'CREATE')).toContain('저장할 수 없으니')
  })

  it('모르는 상태는 지어내지 않고 그대로 보여 줍니다', () => {
    expect(statusName('ARCHIVED')).toBe('ARCHIVED')
  })
})

describe('strategyDiscardsReview', () => {
  it('승인을 버리는 전략만 표시합니다', () => {
    expect(strategyDiscardsReview('APPROVED', 'MERGE')).toBe(true)
    expect(strategyDiscardsReview('APPROVED', 'REPLACE')).toBe(true)
    expect(strategyDiscardsReview('APPROVED', 'SKIP')).toBe(false)
    expect(strategyDiscardsReview('CLOSED', 'REPLACE')).toBe(false)
  })
})
