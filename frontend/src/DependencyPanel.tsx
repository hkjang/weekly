import { useEffect, useState } from 'react'
import { api, del, errorText, post } from './api'
import { Button } from './components'
import type { WorkItemLink, WorkItemLinkView, WorkLookupResponse } from './types'

/**
 * What this task waits on, and what waits on it.
 *
 * A report can say a task is stalled. It has no way to say the task is stalled
 * because another team has not finished something, so the room rediscovers that
 * by asking around every week.
 *
 * One row, read from both ends. Declaring is one-sided — the waiting owner says
 * what they are waiting for, and needs nobody's permission, because a
 * dependency that requires the other team to agree is one nobody records. The
 * reason travels with it so the other team can dispute it.
 */
export default function DependencyPanel({ workItemId, editable, notify, startAdding = false }: {
  workItemId: number
  editable: boolean
  notify: (message: string, kind?: 'success' | 'error') => void
  /**
   * Open on the form rather than on the list. Set where the caller has already
   * asked the question — the report editor offers this beside an issue, and
   * making the reader press a second button named almost the same thing is a
   * click that buys nothing.
   */
  startAdding?: boolean
}) {
  const [view, setView] = useState<WorkItemLinkView>()
  const [adding, setAdding] = useState(startAdding)
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<{ id: number; title: string; displayName: string; organizationName: string }[]>([])
  const [picked, setPicked] = useState<{ id: number; title: string }>()
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)

  const load = () => api<WorkItemLinkView>(`/api/v1/work-items/${workItemId}/links`)
    .then(setView)
    .catch(error => { setView({ blockers: [], blocking: [] }); notify(errorText(error, '의존 관계를 불러올 수 없습니다.'), 'error') })
  useEffect(() => { void load() }, [workItemId])

  // Across the whole deployment, which is the case this exists for: a
  // dependency inside one team is settled by the two people in it.
  //
  // It used to call the work search, which defaults to the caller's own items
  // and refuses an organisation-wide scope to anyone below team leader. So an
  // ordinary contributor searching for the other team's task found nothing, and
  // the only path to declaring a blocker was closed to almost everybody who
  // has one. Measured on real data before this changed: 12 issues written, 0
  // dependencies declared.
  const search = async (text: string) => {
    setQuery(text)
    if (text.trim().length < 2) { setResults([]); return }
    try {
      const found = await api<WorkLookupResponse>(`/api/v1/work-items/lookup?q=${encodeURIComponent(text)}`)
      setResults(found.hits
        .filter(hit => hit.workItemId !== workItemId)
        .map(hit => ({ id: hit.workItemId, title: hit.title, displayName: hit.displayName, organizationName: hit.organizationName })))
    } catch { setResults([]) }
  }

  const add = async () => {
    if (!picked) return
    setBusy(true)
    try {
      await post(`/api/v1/work-items/${workItemId}/links`, { blockerId: picked.id, note })
      setAdding(false); setPicked(undefined); setNote(''); setQuery(''); setResults([])
      await load()
      notify('선행 관계를 등록했습니다.')
    } catch (error) {
      notify(errorText(error, '선행 관계를 등록할 수 없습니다.'), 'error')
    } finally { setBusy(false) }
  }

  const remove = async (link: WorkItemLink) => {
    if (!confirm(`'${link.title}'와의 관계를 삭제하시겠습니까?`)) return
    setBusy(true)
    try { await del(`/api/v1/work-item-links/${link.id}`); await load(); notify('관계를 삭제했습니다.') }
    catch (error) { notify(errorText(error, '관계를 삭제할 수 없습니다.'), 'error') }
    finally { setBusy(false) }
  }

  const row = (link: WorkItemLink, waiting: boolean) => <li key={link.id}
    className={`dependency ${link.completed ? 'done' : waiting ? 'waiting' : 'blocking'}`}>
    <div>
      <strong>{link.title}</strong>
      <small>{link.displayName}{link.organizationName ? ` · ${link.organizationName}` : ''}
        {link.lastWeek ? ` · 최근 ${link.lastWeek}` : ' · 보고 없음'} · 진척 {link.progress}%</small>
      {link.note && <p className="dependency-note">{link.note}</p>}
    </div>
    <div className="dependency-actions">
      {waiting && <span className={`state-chip ${link.completed ? 'done' : 'stalled'}`}>
        {link.completed ? '해소됨' : '대기 중'}</span>}
      {editable && <button className="link-button remove-button" onClick={() => remove(link)}>삭제</button>}
    </div>
  </li>

  if (!view) return null
  const open = view.blockers.filter(link => !link.completed).length
  return <div className="dependency-panel">
    {view.blockers.length === 0 && view.blocking.length === 0 && !adding
      ? <p className="muted">등록된 선행·후행 관계가 없습니다. 다른 업무를 기다리고 있다면 등록해 두면 정체 사유가 기록에 남습니다.</p>
      : <>
        {view.blockers.length > 0 && <>
          <h5>이 업무가 기다리는 것 {open > 0 ? `· 미해소 ${open}건` : '· 모두 해소'}</h5>
          <ul className="dependency-list">{view.blockers.map(link => row(link, true))}</ul>
        </>}
        {view.blocking.length > 0 && <>
          <h5>이 업무를 기다리는 것 {view.blocking.length}건</h5>
          <ul className="dependency-list">{view.blocking.map(link => row(link, false))}</ul>
        </>}
      </>}

    {editable && !adding && <Button variant="secondary" onClick={() => setAdding(true)}>+ 선행 업무 등록</Button>}

    {adding && <div className="dependency-form">
      <label>무엇을 기다리고 있습니까
        <input value={picked ? picked.title : query} readOnly={!!picked}
          onChange={event => search(event.target.value)}
          placeholder="다른 조직의 업무도 두 글자 이상으로 찾을 수 있습니다"/></label>
      {!picked && results.length > 0 && <ul className="dependency-results">
        {results.map(item => <li key={item.id}>
          <button className="link-button" onClick={() => setPicked({ id: item.id, title: item.title })}>
            {item.title}</button>
          <small>{item.displayName}{item.organizationName ? ` · ${item.organizationName}` : ''}</small>
        </li>)}
      </ul>}
      {!picked && query.trim().length >= 2 && results.length === 0 &&
        <p className="muted">일치하는 업무가 없습니다.</p>}
      <label>왜 기다리고 있습니까
        <textarea value={note} maxLength={2000} onChange={event => setNote(event.target.value)}
          placeholder="사유가 없으면 상대 조직이 이의를 제기할 수 없습니다."/></label>
      <div className="dependency-form-actions">
        <Button variant="secondary" disabled={busy}
          onClick={() => { setAdding(false); setPicked(undefined); setQuery(''); setResults([]); setNote('') }}>취소</Button>
        <Button disabled={busy || !picked} onClick={add}>{busy ? '등록 중…' : '등록'}</Button>
      </div>
    </div>}
  </div>
}
