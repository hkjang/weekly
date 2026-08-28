import { useEffect, useMemo, useState } from 'react'
import { errorText, api } from '../api'
import { shiftWeeks } from '../localdate'
import { Button, Card, Empty, PageHeader, Spinner } from '../components'
import DecisionPanel from '../DecisionPanel'
import PresentationMode from '../PresentationMode'
import { meetingNotes, meetingSlides } from '../presentSlides'
import type { MeetingEntry, MeetingView, SessionInfo } from '../types'

/**
 * Meeting mode. Only what the room has to act on: decisions that need this
 * meeting, issues that are new, issues that have outlived their approach,
 * what changed since last week, and what quietly disappeared.
 *
 * Work that continued unchanged is deliberately absent — that is what the
 * written report is for, and repeating it is how status meetings lose the room.
 */

const sectionTone: Record<string, string> = {
  DECISION: 'tone-decision', NEW_ISSUE: 'tone-new', LONG_ISSUE: 'tone-risk',
  CHANGE: 'tone-change', SILENT: 'tone-silent',
}

/**
 * One agenda item, with the room's conclusion recorded against it.
 *
 * The meeting produced the agenda and then stopped. Whatever was concluded went
 * into someone's notebook, and next Monday the person whose task it was opened
 * a blank editor with no sign of it. The decision is written here, while the
 * item is on screen and the wording is still fresh, and the follow-up comes
 * back to its owner in the report editor.
 */
function EntryRow({ entry, aiEnabled, notify }: {
  entry: MeetingEntry
  aiEnabled: boolean
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [recording, setRecording] = useState(false)
  return <li>
    <div className="meeting-entry-head">
      <strong>{entry.title}</strong>
      <span className="muted-chip">{entry.displayName}{entry.organizationName ? ` · ${entry.organizationName}` : ''}</span>
      {entry.progressDelta !== 0 && <span className={entry.progressDelta > 0 ? 'delta-up' : 'delta-down'}>
        {entry.progressDelta > 0 ? `+${entry.progressDelta}` : entry.progressDelta}%</span>}
      <button className="link-button" onClick={() => setRecording(current => !current)}>
        {recording ? '결정 접기' : '결정 기록'}</button>
    </div>
    {entry.detail && <p className="meeting-detail">{entry.detail}</p>}
    {entry.note && <p className="muted">{entry.note}</p>}
    {recording && <div className="meeting-decisions">
      <DecisionPanel workItemId={entry.workItemId} aiEnabled={aiEnabled} notify={notify} />
    </div>}
  </li>
}

export default function MeetingPage({ session, notify }: {
  session: SessionInfo
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const canTeam = session.user.role !== 'USER'
  const [scope, setScope] = useState<'SELF' | 'TEAM'>(canTeam ? 'TEAM' : 'SELF')
  const [week, setWeek] = useState(session.currentWeekStart)
  const [view, setView] = useState<MeetingView>()
  const [presenting, setPresenting] = useState(false)

  // A failed load used to become an empty view, and an empty view is a claim:
  // the reader is told there is nothing, which is a different thing from not
  // knowing. The toast that said otherwise fades. Measured by failing this
  // screen's own request in a browser: the page settled on 없습니다 with the
  // numbers zeroed, and nothing on it said the load had failed.
  const [failed, setFailed] = useState('')
  const [reload, setReload] = useState(0)
  useEffect(() => {
    let stale = false
    setView(undefined)
    setFailed('')
    api<MeetingView>(`/api/v1/meeting?scope=${scope}&week=${week}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setFailed(errorText(error, '회의 자료를 불러올 수 없습니다.'))
        notify(errorText(error, '회의 자료를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [scope, week, reload])

  // Counted from what exists, not from what arrived: the summary line above
  // the agenda would otherwise report the cap back as the number of items.
  const total = useMemo(() => view?.sections.reduce((sum, section) => sum + section.total, 0) ?? 0, [view])
  const active = useMemo(() => (view?.sections ?? []).filter(section => section.entries.length > 0), [view])

  return <>
    <PageHeader title="회의 모드" description="이번 주 회의에서 다뤄야 할 것만 모았습니다. 변화가 없는 업무는 표시하지 않습니다."
      action={<div className="header-actions">
        <button className="button secondary" onClick={() => setWeek(shiftWeeks(week, -1))}>◀ 이전 주</button>
        <input type="date" aria-label="회의 주차" value={week} onChange={event => event.target.value && setWeek(event.target.value)} />
        <button className="button secondary" onClick={() => setWeek(shiftWeeks(week, 1))}>다음 주 ▶</button>
        {canTeam && <select aria-label="조회 범위" value={scope} onChange={event => setScope(event.target.value as 'SELF' | 'TEAM')}>
          <option value="TEAM">조직 전체</option>
          <option value="SELF">내 업무</option>
        </select>}
        <button className="button" disabled={!total} onClick={() => setPresenting(true)}>▶ 발표 모드</button>
      </div>} />

    {failed ? <Card><Empty>{failed}</Empty><div className="audit-pager"><Button variant="secondary" onClick={() => setReload(n => n + 1)}>다시 시도</Button></div></Card> : view === undefined ? <Spinner/> : <>
      <div className="meeting-summary">
        <span><strong>{view.workItems}</strong>건의 업무</span>
        <span><strong>{view.people}</strong>명</span>
        <span><strong>{total}</strong>건의 안건</span>
        <span className="muted">{view.previousWeek} 대비</span>
      </div>
      {total === 0
        ? <Empty>이번 주에는 회의에서 다룰 변화가 없습니다. 지난주와 달라진 점, 새 이슈, 결정 요청이 모두 없습니다.</Empty>
        : active.map(section => <Card key={section.key} title={section.title}
            action={<span className="muted-chip">{section.total > section.entries.length
              ? `${section.total}건 중 ${section.entries.length}건`
              : `${section.entries.length}건`}</span>}>
            <p className="muted">{section.purpose}</p>
            {/* An agenda that quietly prints part of everything is worse than
                one that prints everything. If rows were left out, the heading
                says so and says how the surviving ones were chosen. */}
            {section.total > section.entries.length && <p className="warn-text">
              {section.total}건 중 변화가 큰 {section.entries.length}건만 실었습니다.
              진척이 뒤로 간 업무, 그다음 변화 폭이 큰 순서입니다. 전체는 팀 주간보고에서 확인하십시오.
            </p>}
            <ul className={`meeting-list ${sectionTone[section.key] ?? ''}`}>
              {section.entries.map(entry => <EntryRow key={`${section.key}-${entry.workItemId}`} entry={entry} aiEnabled={session.aiEnabled} notify={notify} />)}
            </ul>
          </Card>)}
    </>}

    {presenting && view && <PresentationMode slides={meetingSlides(view)} label={`${view.week} 주차 회의`}
      notes={meetingNotes} onClose={() => setPresenting(false)} />}
  </>
}
