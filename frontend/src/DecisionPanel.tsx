import { useEffect, useState } from 'react'
import { api, del, errorText, patch, post } from './api'
import { Button, Empty, Spinner } from './components'
import type { Decision, DecisionInput, DecisionStatus } from './types'

/**
 * The decisions taken about one task.
 *
 * A weekly report records what happened; it has nowhere to say who decided
 * what, on what grounds, or what was meant to follow. The question people
 * actually ask months later — 왜 이렇게 하기로 했더라 — was answered by finding
 * someone who still remembered, and the handover screen, built for the moment
 * nobody does, had nothing to show.
 *
 * Written by hand on purpose. The roadmap settles the order: the explicit
 * record first, suggestion from report text afterwards. A log whose
 * completeness depends on a model noticing is not a log.
 */

const statusName: Record<DecisionStatus, string> = {
  OPEN: '후속 조치 중', DONE: '완료', SUPERSEDED: '대체됨',
}

const blank = (): DecisionInput => ({
  title: '', decidedBy: '', decidedOn: new Date().toISOString().slice(0, 10),
  rationale: '', followUp: '', dueDate: '', status: 'OPEN',
})

export default function DecisionPanel({ workItemId, notify }: {
  workItemId: number
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [decisions, setDecisions] = useState<Decision[]>()
  const [draft, setDraft] = useState<DecisionInput>()
  const [editingId, setEditingId] = useState<number>()
  const [busy, setBusy] = useState(false)

  const load = () => api<Decision[]>(`/api/v1/work-items/${workItemId}/decisions`)
    .then(setDecisions)
    .catch(error => { setDecisions([]); notify(errorText(error, '결정 기록을 불러올 수 없습니다.'), 'error') })
  useEffect(() => { void load() }, [workItemId])

  const save = async () => {
    if (!draft) return
    setBusy(true)
    try {
      if (editingId) await patch(`/api/v1/decisions/${editingId}`, draft)
      else await post(`/api/v1/work-items/${workItemId}/decisions`, draft)
      setDraft(undefined); setEditingId(undefined)
      await load()
      notify(editingId ? '결정 기록을 수정했습니다.' : '결정을 기록했습니다.')
    } catch (error) {
      notify(errorText(error, '결정을 기록할 수 없습니다.'), 'error')
    } finally { setBusy(false) }
  }

  const remove = async (decision: Decision) => {
    if (!confirm(`'${decision.title}' 기록을 삭제하시겠습니까? 삭제한 기록은 복구할 수 없습니다.`)) return
    setBusy(true)
    try { await del(`/api/v1/decisions/${decision.id}`); await load(); notify('결정 기록을 삭제했습니다.') }
    catch (error) { notify(errorText(error, '결정 기록을 삭제할 수 없습니다.'), 'error') }
    finally { setBusy(false) }
  }

  // Superseding is stated by the new entry, so recording one starts from the
  // entry it replaces: the reader should not have to reconstruct the chain.
  const supersede = (decision: Decision) => {
    setEditingId(undefined)
    setDraft({ ...blank(), title: decision.title, decidedBy: decision.decidedBy, supersedesId: decision.id })
  }

  const edit = (decision: Decision) => {
    setEditingId(decision.id)
    setDraft({
      title: decision.title, decidedBy: decision.decidedBy, decidedOn: decision.decidedOn,
      rationale: decision.rationale, followUp: decision.followUp,
      dueDate: decision.dueDate ?? '', status: decision.status,
    })
  }

  if (decisions === undefined) return <Spinner/>
  return <div className="decision-panel">
    {decisions.length === 0
      ? <Empty action={<Button variant="secondary" onClick={() => { setEditingId(undefined); setDraft(blank()) }}>결정 기록하기</Button>}>
          이 업무에 기록된 결정이 없습니다. 누가 무엇을 왜 정했는지 적어 두면 인수인계와 기간 보고에서 함께 보입니다.
        </Empty>
      : <ul className="decision-list">{decisions.map(decision => <li key={decision.id}
          className={`decision ${decision.status.toLowerCase()}`}>
          <div className="decision-head">
            <strong>{decision.title}</strong>
            <span className={`decision-status ${decision.status.toLowerCase()}`}>{statusName[decision.status]}</span>
          </div>
          <div className="decision-facts">
            <span><b>{decision.decidedBy}</b> 결정</span>
            <span>{decision.decidedOn}</span>
            {decision.dueDate && <span>후속 기한 {decision.dueDate}</span>}
            {decision.recordedByName && <span className="muted">{decision.recordedByName} 기록</span>}
            {decision.supersedesId && <span className="muted">이전 결정 #{decision.supersedesId} 대체</span>}
          </div>
          {decision.rationale && <p className="decision-rationale"><b>근거</b> {decision.rationale}</p>}
          {decision.followUp && <p className="decision-followup"><b>후속 조치</b> {decision.followUp}</p>}
          <div className="decision-actions">
            <button className="link-button" onClick={() => edit(decision)}>수정</button>
            {decision.status !== 'SUPERSEDED' &&
              <button className="link-button" onClick={() => supersede(decision)}>이 결정을 대체</button>}
            <button className="link-button remove-button" onClick={() => remove(decision)}>삭제</button>
          </div>
        </li>)}</ul>}

    {decisions.length > 0 && !draft &&
      <Button variant="secondary" onClick={() => { setEditingId(undefined); setDraft(blank()) }}>+ 결정 기록</Button>}

    {draft && <div className="decision-form">
      <h5>{editingId ? '결정 기록 수정' : draft.supersedesId ? `이전 결정 #${draft.supersedesId}을(를) 대체하는 결정` : '새 결정 기록'}</h5>
      <label>무엇을 정했는지<input value={draft.title} maxLength={240}
        onChange={event => setDraft({ ...draft, title: event.target.value })} placeholder="예: 외부 연동을 자체 구현으로 전환"/></label>
      <div className="decision-form-row">
        <label>결정한 사람<input value={draft.decidedBy} maxLength={120}
          onChange={event => setDraft({ ...draft, decidedBy: event.target.value })} placeholder="예: 김상무"/></label>
        <label>결정 일자<input type="date" value={draft.decidedOn}
          onChange={event => setDraft({ ...draft, decidedOn: event.target.value })}/></label>
        <label>후속 기한<input type="date" value={draft.dueDate}
          onChange={event => setDraft({ ...draft, dueDate: event.target.value })}/></label>
        <label>상태<select value={draft.status}
          onChange={event => setDraft({ ...draft, status: event.target.value as DecisionStatus })}>
          <option value="OPEN">후속 조치 중</option>
          <option value="DONE">완료</option>
        </select></label>
      </div>
      <label>왜 그렇게 정했는지<textarea value={draft.rationale} maxLength={5000}
        onChange={event => setDraft({ ...draft, rationale: event.target.value })}
        placeholder="근거가 없는 결정은 나중에 다시 논의할 수 없습니다."/></label>
      <label>무엇을 하기로 했는지<textarea value={draft.followUp} maxLength={5000}
        onChange={event => setDraft({ ...draft, followUp: event.target.value })}/></label>
      <div className="decision-form-actions">
        <Button variant="secondary" disabled={busy} onClick={() => { setDraft(undefined); setEditingId(undefined) }}>취소</Button>
        <Button disabled={busy || !draft.title.trim() || !draft.decidedBy.trim()} onClick={save}>
          {busy ? '저장 중…' : editingId ? '수정 저장' : '기록'}</Button>
      </div>
    </div>}
  </div>
}
