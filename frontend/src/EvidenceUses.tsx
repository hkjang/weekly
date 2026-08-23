import { useEffect, useState } from 'react'
import { api, errorText } from './api'
import { Spinner } from './components'
import type { EvidenceUseView } from './types'

/**
 * What else was written from this source.
 *
 * Forward lineage tells a reader of one report where its lines came from. This
 * is the other direction: the owner of a page, a deck or a commit finding out
 * what was built on it — which is who finds out last, and who most needs to
 * know before changing it.
 *
 * Scoped like everything else, so the list names only what the reader may
 * already open.
 */
export default function EvidenceUses({ kind, reference, label, notify, onClose }: {
  kind: string
  reference: string
  label: string
  notify: (message: string, kind?: 'success' | 'error') => void
  onClose: () => void
}) {
  const [view, setView] = useState<EvidenceUseView>()

  useEffect(() => {
    let stale = false
    api<EvidenceUseView>(`/api/v1/evidence/uses?kind=${encodeURIComponent(kind)}&reference=${encodeURIComponent(reference)}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ kind, reference, uses: [], total: 0, limit: 0 })
        notify(errorText(error, '근거 사용처를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [kind, reference])

  return <div className="evidence-uses">
    <div className="evidence-uses-head">
      <strong>{view?.title || label}을(를) 근거로 쓴 보고</strong>
      <button className="link-button" onClick={onClose}>닫기</button>
    </div>
    {view === undefined ? <Spinner/> : view.uses.length === 0
      ? <p className="muted">조회 권한 범위 안에서 이 근거를 쓴 다른 보고가 없습니다.</p>
      : <>
        <ul className="evidence-use-list">{view.uses.map(use => <li key={use.reportItemId}>
          <span>{use.weekStart}</span>
          <strong>{use.title}</strong>
          <small>{use.displayName}{use.organizationName ? ` · ${use.organizationName}` : ''}
            {use.detail ? ` · ${use.detail}` : ''}</small>
        </li>)}</ul>
        {view.total > view.uses.length && <p className="muted capped-note">
          {view.total}건 중 {view.uses.length}건만 보여 줍니다.</p>}
      </>}
  </div>
}
