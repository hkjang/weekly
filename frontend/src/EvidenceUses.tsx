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
  const [loadingMore, setLoadingMore] = useState(false)

  useEffect(() => {
    let stale = false
    api<EvidenceUseView>(`/api/v1/evidence/uses?kind=${encodeURIComponent(kind)}&reference=${encodeURIComponent(reference)}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ kind, reference, uses: [], total: 0, limit: 0, offset: 0 })
        notify(errorText(error, '근거 사용처를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [kind, reference])

  /**
   * 뒤쪽으로 가는 길.
   *
   * 이 목록은 "50건 중 12건만 보여 줍니다" 라고 말하고 거기서 끝났습니다. 이
   * 패널을 여는 사람은 자기 페이지를 고치기 전에 **영향받는 곳을 빠짐없이**
   * 찾으려고 열며, 그 사람에게 뒤쪽은 부록이 아니라 찾으러 온 것입니다.
   */
  const more = async () => {
    if (!view || loadingMore) return
    setLoadingMore(true)
    try {
      const next = await api<EvidenceUseView>(
        `/api/v1/evidence/uses?kind=${encodeURIComponent(kind)}&reference=${encodeURIComponent(reference)}`
        + `&offset=${view.uses.length}`)
      setView({ ...next, uses: [...view.uses, ...next.uses] })
    } catch (error) {
      notify(errorText(error, '다음 목록을 불러올 수 없습니다.'), 'error')
    } finally {
      setLoadingMore(false)
    }
  }

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
          {view.total}건 중 {view.uses.length}건까지 보여 줍니다.{' '}
          <button className="link-button" onClick={more} disabled={loadingMore}>
            {loadingMore ? '불러오는 중…' : '더 보기'}</button></p>}
      </>}
  </div>
}
