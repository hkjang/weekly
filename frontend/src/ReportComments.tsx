import { useState } from 'react'
import { errorText, post } from './api'
import { Button } from './components'
import type { ReportComment } from './types'

/**
 * Asking about a report without sending it back.
 *
 * The workflow had exactly two things a reviewer could do: approve, or reject.
 * A question — "이 이슈 어디까지 진행됐습니까" — had no home, so asking meant
 * rejecting, which changes the report's status and puts the work back on
 * somebody's desk over a question. Most reviewers said nothing instead.
 *
 * The endpoint for this was already written, with its permission check, its
 * length limit and its audit entry. Nothing called it. The author's screen even
 * displayed 검토 의견 — a box that could only ever be filled by a rejection, and
 * that they could not answer.
 *
 * So it is a conversation now, on both sides, and it is deliberately small: a
 * weekly report is not a chat room. What it needs is somewhere to put the one
 * question that would otherwise become a rejection.
 */
export default function ReportComments({ reportId, comments, onPosted, notify, placeholder }: {
  reportId: number
  comments: ReportComment[]
  onPosted: () => Promise<void> | void
  notify: (message: string, kind?: 'success' | 'error') => void
  placeholder?: string
}) {
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)

  const send = async () => {
    const content = draft.trim()
    if (!content) return
    setBusy(true)
    try {
      await post(`/api/v1/reports/${reportId}/comments`, { content })
      setDraft('')
      await onPosted()
      notify('의견을 남겼습니다.')
    } catch (error) {
      notify(errorText(error, '의견을 남길 수 없습니다.'), 'error')
    } finally { setBusy(false) }
  }

  return <div className="report-comments">
    {comments.length > 0 && <div className="comments">
      {comments.map(comment => <div key={comment.id}>
        <strong>{comment.displayName}</strong>
        <span>{new Date(comment.createdAt).toLocaleString('ko-KR')}</span>
        <p>{comment.content}</p>
      </div>)}
    </div>}
    <div className="comment-form">
      <textarea value={draft} maxLength={5000} disabled={busy}
        onChange={event => setDraft(event.target.value)}
        aria-label="검토 의견"
        placeholder={placeholder ?? '질문이나 의견을 남기세요. 보고서 상태는 바뀌지 않습니다.'} />
      <Button onClick={send} disabled={busy || !draft.trim()}>{busy ? '남기는 중…' : '의견 남기기'}</Button>
    </div>
  </div>
}
