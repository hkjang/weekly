/**
 * reviewDecision reads what the reviewer typed into the prompt and says what
 * should happen next.
 *
 * `prompt()` answers `null` when somebody cancels and `''` when they press OK
 * with the box empty. The screen treated both the same: it returned, said
 * nothing, and left the report exactly as it was. Cancelling silently is
 * right — they changed their mind. Pressing OK is an attempt, and an attempt
 * that does nothing has to say why, or the reviewer clicks 반려 again and
 * again wondering which part of the screen is broken.
 *
 * Found by walking the review loop on a running deployment: the first attempt
 * to reject a report did nothing at all, and nothing on screen explained it.
 */
export type ReviewDecision =
  | { kind: 'send'; comment: string }
  | { kind: 'cancelled' }
  | { kind: 'needs-reason'; message: string }

export function reviewDecision(action: 'approve' | 'reject', typed: string | null): ReviewDecision {
  if (typed === null) return { kind: 'cancelled' }
  const comment = typed.trim()
  if (action === 'reject' && comment === '') {
    return { kind: 'needs-reason', message: '반려 사유를 입력해야 반려할 수 있습니다. 작성자가 무엇을 고쳐야 하는지 알 수 있게 적어 주세요.' }
  }
  // 승인 의견은 선택입니다. 빈 채로 보내면 상태만 바뀝니다.
  return { kind: 'send', comment }
}
