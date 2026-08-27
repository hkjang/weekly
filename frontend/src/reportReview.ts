import type { Report, ReportComment } from './types'

/**
 * The reason a report was sent back.
 *
 * A rejection is recorded twice by the server: once in the status history,
 * which nothing reads, and once as a comment on the report, which is what the
 * writer sees. So the reason is a comment, and picking it out is a rule rather
 * than a field: whoever sent it back is not its author, and if they wrote more
 * than once the last thing they said is the one to act on.
 *
 * Comments arrive oldest first.
 */
export function revisionReasonOf(report: Pick<Report, 'userId' | 'comments'> | undefined | null): ReportComment | undefined {
  if (!report) return undefined
  for (let index = report.comments.length - 1; index >= 0; index--) {
    if (report.comments[index].userId !== report.userId) return report.comments[index]
  }
  return undefined
}
