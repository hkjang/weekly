import { useEffect, useState } from 'react'
import { api } from './api'
import PresentationMode from './PresentationMode'
import { reportSlides } from './presentSlides'
import type { Report, ReportAttachment } from './types'

/**
 * Presents one weekly report, captures included.
 *
 * The attachment list is a separate request, so it is fetched here rather than
 * in each of the three screens that can present a report. A capture that fails
 * to load must not stop the presentation: the written content is the point, and
 * a meeting is the worst moment to be blocked by a missing image.
 */
export default function ReportPresentation({ report, label, onClose }: {
  report: Report
  label: string
  onClose: () => void
}) {
  const [attachments, setAttachments] = useState<ReportAttachment[]>()

  useEffect(() => {
    let stale = false
    api<ReportAttachment[]>(`/api/v1/reports/${report.id}/attachments`)
      .then(value => { if (!stale) setAttachments(value) })
      .catch(() => { if (!stale) setAttachments([]) })
    return () => { stale = true }
  }, [report.id])

  // Showing the deck without captures while they load would renumber the
  // slides underneath the presenter, so wait for the answer either way.
  if (attachments === undefined) {
    return <div className="presentation presentation-loading" role="dialog" aria-label={`${label} 발표 준비`}>
      <div className="spinner" /><span>발표 자료를 준비하고 있습니다…</span>
    </div>
  }

  return <PresentationMode label={label} slides={reportSlides(report, attachments)} onClose={onClose} />
}
