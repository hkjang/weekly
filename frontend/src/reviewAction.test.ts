import { describe, expect, it } from 'vitest'
import { reviewDecision } from './reviewAction'

// Walked on a running deployment: pressing 반려 and confirming an empty prompt
// did nothing, said nothing, and left the report SUBMITTED.

describe('reviewDecision', () => {
  it('취소는 조용합니다', () => {
    expect(reviewDecision('reject', null)).toEqual({ kind: 'cancelled' })
    expect(reviewDecision('approve', null)).toEqual({ kind: 'cancelled' })
  })

  it('사유 없이 반려하려 하면 왜 안 되는지 말합니다', () => {
    const decision = reviewDecision('reject', '   ')
    expect(decision.kind).toBe('needs-reason')
    if (decision.kind === 'needs-reason') expect(decision.message).toContain('반려 사유')
  })

  it('사유가 있으면 다듬어 보냅니다', () => {
    expect(reviewDecision('reject', '  담당자가 없습니다  ')).toEqual({ kind: 'send', comment: '담당자가 없습니다' })
  })

  it('승인 의견은 비어 있어도 보냅니다', () => {
    expect(reviewDecision('approve', '')).toEqual({ kind: 'send', comment: '' })
    expect(reviewDecision('approve', ' 좋습니다 ')).toEqual({ kind: 'send', comment: '좋습니다' })
  })
})
