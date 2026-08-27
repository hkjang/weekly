import { describe, expect, it } from 'vitest'
import { revisionReasonOf } from './reportReview'

// The writer is told their report came back, and this decides what they are
// told it came back for. Picking the wrong comment shows them their own words
// as the reviewer's, which is worse than showing nothing.

const comment = (id: number, userId: number, content: string) =>
  ({ id, userId, displayName: `사용자 ${userId}`, content, createdAt: `2026-08-2${id}T09:00:00Z` })

describe('revisionReasonOf', () => {
  it('반려한 사람이 남긴 말을 고릅니다', () => {
    const report = { userId: 7, comments: [comment(1, 9, '이슈에 담당이 없습니다')] }
    expect(revisionReasonOf(report)?.content).toBe('이슈에 담당이 없습니다')
  })

  it('검토자가 여러 번 말했으면 마지막 말을 고릅니다', () => {
    const report = { userId: 7, comments: [comment(1, 9, '먼저 한 말'), comment(2, 9, '나중에 한 말')] }
    expect(revisionReasonOf(report)?.content).toBe('나중에 한 말')
  })

  // The writer answers in the same thread, so their reply is the newest comment
  // on the report. Taking the newest one would quote the writer to themselves.
  it('작성자 자신이 마지막에 답했어도 검토자의 말을 고릅니다', () => {
    const report = { userId: 7, comments: [comment(1, 9, '검토자가 남긴 사유'), comment(2, 7, '작성자가 단 답글')] }
    expect(revisionReasonOf(report)?.content).toBe('검토자가 남긴 사유')
  })

  it('작성자의 말밖에 없으면 아무것도 고르지 않습니다', () => {
    const report = { userId: 7, comments: [comment(1, 7, '내가 쓴 메모')] }
    expect(revisionReasonOf(report)).toBeUndefined()
  })

  it('의견이 없거나 보고서가 없으면 아무것도 고르지 않습니다', () => {
    expect(revisionReasonOf({ userId: 7, comments: [] })).toBeUndefined()
    expect(revisionReasonOf(undefined)).toBeUndefined()
  })
})
