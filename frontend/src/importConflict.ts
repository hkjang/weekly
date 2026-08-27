/**
 * importConflict says what confirming an Import will do to a report that is
 * already there.
 *
 * The screen used to say only "동일 주차 보고서 #364 (APPROVED)가 있습니다."
 * — the raw enum in an otherwise Korean screen, and no word about the
 * consequence. Both merging and replacing set the report back to 확정 and
 * clear its review stamp (see confirmImportJob), so an approved report loses
 * its approval on the strategy the screen picks by default, silently.
 *
 * Nobody would object to the rule; they object to finding out afterwards.
 */

export type ImportStrategy = 'CREATE' | 'MERGE' | 'REPLACE' | 'SKIP'

const reportStatusName: Record<string, string> = {
  DRAFT: '작성 중', SUBMITTED: '검토 대기', REVISION_REQUESTED: '반려/수정',
  APPROVED: '승인', CLOSED: '확정',
}

/** statusName translates a report status, and leaves an unknown one legible. */
export function statusName(status: string): string {
  return reportStatusName[status] ?? status
}

/** reviewed reports carry a stamp that importing over them throws away. */
const reviewed = new Set(['SUBMITTED', 'REVISION_REQUESTED', 'APPROVED'])

/**
 * conflictWarning is the sentence shown beside the strategy picker, or an
 * empty string when there is no report in the way.
 */
export function conflictWarning(reportID: number | undefined, status: string, strategy: ImportStrategy): string {
  if (!reportID) return ''
  const head = `동일 주차 보고서 #${reportID}(${statusName(status)})가 있습니다.`
  if (strategy === 'SKIP') return `${head} 건너뛰면 그대로 둡니다.`
  if (strategy === 'CREATE') return `${head} 신규 생성으로는 저장할 수 없으니 병합·교체·건너뛰기를 고르세요.`
  const effect = strategy === 'REPLACE'
    ? '기존 업무 항목을 모두 지우고 이 파일의 내용으로 바꿉니다.'
    : '이 파일의 업무 항목을 기존 보고서에 더합니다.'
  if (!reviewed.has(status)) return `${head} ${effect}`
  return `${head} ${effect} 저장하면 상태가 확정으로 돌아가고 ${statusName(status)} 기록은 사라집니다.`
}

/**
 * strategyDiscardsReview is true when confirming would throw a review stamp
 * away, so the screen can mark the sentence rather than only print it.
 */
export function strategyDiscardsReview(status: string, strategy: ImportStrategy): boolean {
  return (strategy === 'MERGE' || strategy === 'REPLACE') && reviewed.has(status)
}
