import { useEffect, useMemo, useState } from 'react'
import { errorText, api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
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

function shiftWeek(week: string, deltaWeeks: number): string {
  const date = new Date(`${week}T00:00:00`)
  date.setDate(date.getDate() + deltaWeeks * 7)
  return date.toISOString().slice(0, 10)
}

function EntryRow({ entry }: { entry: MeetingEntry }) {
  return <li>
    <div className="meeting-entry-head">
      <strong>{entry.title}</strong>
      <span className="muted-chip">{entry.displayName}{entry.organizationName ? ` · ${entry.organizationName}` : ''}</span>
      {entry.progressDelta !== 0 && <span className={entry.progressDelta > 0 ? 'delta-up' : 'delta-down'}>
        {entry.progressDelta > 0 ? `+${entry.progressDelta}` : entry.progressDelta}%</span>}
    </div>
    {entry.detail && <p className="meeting-detail">{entry.detail}</p>}
    {entry.note && <p className="muted">{entry.note}</p>}
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

  useEffect(() => {
    let stale = false
    setView(undefined)
    api<MeetingView>(`/api/v1/meeting?scope=${scope}&week=${week}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ week, previousWeek: '', scope, people: 0, workItems: 0, sections: [] })
        notify(errorText(error, '회의 자료를 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [scope, week])

  const total = useMemo(() => view?.sections.reduce((sum, section) => sum + section.entries.length, 0) ?? 0, [view])
  const active = useMemo(() => (view?.sections ?? []).filter(section => section.entries.length > 0), [view])

  return <>
    <PageHeader title="회의 모드" description="이번 주 회의에서 다뤄야 할 것만 모았습니다. 변화가 없는 업무는 표시하지 않습니다."
      action={<div className="header-actions">
        <button className="button secondary" onClick={() => setWeek(shiftWeek(week, -1))}>◀ 이전 주</button>
        <input type="date" value={week} onChange={event => event.target.value && setWeek(event.target.value)} />
        <button className="button secondary" onClick={() => setWeek(shiftWeek(week, 1))}>다음 주 ▶</button>
        {canTeam && <select value={scope} onChange={event => setScope(event.target.value as 'SELF' | 'TEAM')}>
          <option value="TEAM">조직 전체</option>
          <option value="SELF">내 업무</option>
        </select>}
        <button className="button" disabled={!total} onClick={() => setPresenting(true)}>▶ 발표 모드</button>
      </div>} />

    {view === undefined ? <Spinner/> : <>
      <div className="meeting-summary">
        <span><strong>{view.workItems}</strong>건의 업무</span>
        <span><strong>{view.people}</strong>명</span>
        <span><strong>{total}</strong>건의 안건</span>
        <span className="muted">{view.previousWeek} 대비</span>
      </div>
      {total === 0
        ? <Empty>이번 주에는 회의에서 다룰 변화가 없습니다. 지난주와 달라진 점, 새 이슈, 결정 요청이 모두 없습니다.</Empty>
        : active.map(section => <Card key={section.key} title={section.title}
            action={<span className="muted-chip">{section.entries.length}건</span>}>
            <p className="muted">{section.purpose}</p>
            <ul className={`meeting-list ${sectionTone[section.key] ?? ''}`}>
              {section.entries.map(entry => <EntryRow key={`${section.key}-${entry.workItemId}`} entry={entry} />)}
            </ul>
          </Card>)}
    </>}

    {presenting && view && <PresentationMode slides={meetingSlides(view)} label={`${view.week} 주차 회의`}
      notes={meetingNotes} onClose={() => setPresenting(false)} />}
  </>
}
