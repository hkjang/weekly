import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, del, errorText, post, put } from '../api'
import { Button, Card, Empty, Modal, PageHeader } from '../components'
import { todayLocal } from '../localdate'
import {
  addDays, boardAfterFailure, byAssignee, doneRatio, gridRange, monthGrid, monthLabel, monthStart,
  priorityLabels, priorityOrder, shiftMonths, taskState, tasksOnDay, weekDays, weekdayNames,
} from '../scheduleGrid'
import type {
  ITSMLookup, ReportInclusionPreference, ScheduleBoard, SchedulePriority, ScheduleTask, SessionInfo,
} from '../types'

/**
 * 월간 업무 상황판.
 *
 * A department's month, with a checkbox on every line. Four ways to read the
 * same data — the grid for planning, the week for standing in front of, the
 * list for working through, the lanes for "who is carrying what" — because the
 * same month is asked four different questions and one layout answers only one
 * of them.
 *
 * The board is also a wall display: 전체화면 puts it on a screen in the office,
 * where it refreshes itself so that what is hanging there is not yesterday.
 */

type ViewName = 'month' | 'week' | 'list' | 'people'

const views: { id: ViewName; label: string }[] = [
  { id: 'month', label: '월' }, { id: 'week', label: '주' },
  { id: 'list', label: '목록' }, { id: 'people', label: '담당자' },
]

const emptyDraft = (day: string): Draft => ({
  id: 0, title: '', category: '', startDate: day, endDate: day,
  priority: 'NORMAL', assigneeId: 0, srId: '', note: '',
})

interface Draft {
  id: number; title: string; category: string
  startDate: string; endDate: string
  priority: SchedulePriority; assigneeId: number; srId: string; note: string
}

export default function SchedulePage({ session, notify }: {
  session: SessionInfo
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [anchor, setAnchor] = useState(todayLocal())
  const [view, setView] = useState<ViewName>('month')
  const [scope, setScope] = useState<'SELF' | 'TEAM'>('TEAM')
  const [hideDone, setHideDone] = useState(false)
  const [board, setBoard] = useState<ScheduleBoard>()
  const [failed, setFailed] = useState('')
  const [draft, setDraft] = useState<Draft>()
  const [members, setMembers] = useState<{ id: number; displayName: string; organizationName: string }[]>([])
  const [fullscreen, setFullscreen] = useState(false)
  const boardRef = useRef<HTMLDivElement>(null)

  // The week view walks by weeks and everything else by months, so the range
  // follows the view rather than the other way round.
  const weeks = useMemo(() => monthGrid(anchor), [anchor])
  const range = useMemo(() => {
    if (view === 'week') {
      const days = weekDays(anchor)
      return { from: days[0], to: days[6] }
    }
    return gridRange(weeks)
  }, [view, anchor, weeks])

  const load = useCallback(async () => {
    try {
      const value = await api<ScheduleBoard>(
        `/api/v1/schedule?from=${range.from}&to=${range.to}&scope=${scope}`)
      setBoard(value); setFailed('')
    } catch (error) {
      // An empty board and a board that failed to load look identical, and the
      // difference is whether the department has nothing planned or cannot see
      // what it planned. Whatever is on the screen is kept only while it is
      // still an answer to the question on the screen — see boardAfterFailure.
      setBoard(current => boardAfterFailure(current, range.from, range.to, scope))
      setFailed(errorText(error, '업무 일정을 불러오지 못했습니다.'))
    }
  }, [range.from, range.to, scope])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    api<ReportInclusionPreference>('/api/v1/me/report-inclusions')
      .then(value => setMembers(value.members ?? []))
      .catch(() => setMembers([]))
  }, [])

  // A board on a wall is read by people who are not touching it. Left alone it
  // would show the morning's plan all afternoon, so it re-reads itself while it
  // is full screen — and only then, because a laptop does not need it.
  useEffect(() => {
    if (!fullscreen) return
    const timer = window.setInterval(() => { void load() }, 60_000)
    return () => window.clearInterval(timer)
  }, [fullscreen, load])

  useEffect(() => {
    const sync = () => setFullscreen(Boolean(document.fullscreenElement))
    document.addEventListener('fullscreenchange', sync)
    return () => document.removeEventListener('fullscreenchange', sync)
  }, [])

  const toggleFullscreen = () => {
    const element = boardRef.current
    if (!element) return
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => undefined)
    else void element.requestFullscreen?.().catch(() => notify('전체화면으로 전환할 수 없습니다.', 'error'))
  }

  const today = board?.today ?? todayLocal()
  const tasks = useMemo(() => {
    const all = board?.tasks ?? []
    return hideDone ? all.filter(task => !task.done) : all
  }, [board, hideDone])

  // Ticking a box redraws the line before the request finishes, and puts it
  // back if the server disagrees. At a stand-up the wait is the whole cost.
  const setDone = async (task: ScheduleTask, done: boolean) => {
    setBoard(current => current && ({
      ...current,
      tasks: current.tasks.map(item => item.id === task.id ? { ...item, done } : item),
    }))
    try {
      await post(`/api/v1/schedule/${task.id}/done`, { done })
      await load()
    } catch (error) {
      setBoard(current => current && ({
        ...current,
        tasks: current.tasks.map(item => item.id === task.id ? { ...item, done: !done } : item),
      }))
      notify(errorText(error, '완료 여부를 저장하지 못했습니다.'), 'error')
    }
  }

  const save = async (value: Draft) => {
    const body = {
      title: value.title.trim(), category: value.category.trim(),
      startDate: value.startDate, endDate: value.endDate || value.startDate,
      priority: value.priority, note: value.note.trim(),
      srId: value.srId.trim(), assigneeId: value.assigneeId || undefined,
    }
    try {
      if (value.id) await put(`/api/v1/schedule/${value.id}`, body)
      else await post('/api/v1/schedule', body)
      setDraft(undefined)
      await load()
      notify(value.id ? '일정을 수정했습니다.' : '일정을 추가했습니다.')
    } catch (error) { notify(errorText(error, '일정을 저장하지 못했습니다.'), 'error') }
  }

  const remove = async (value: Draft) => {
    if (!confirm('이 일정을 삭제하시겠습니까?')) return
    try {
      await del(`/api/v1/schedule/${value.id}`)
      setDraft(undefined)
      await load()
      notify('일정을 삭제했습니다.')
    } catch (error) { notify(errorText(error, '일정을 삭제하지 못했습니다.'), 'error') }
  }

  const openDay = (day: string) => setDraft(emptyDraft(day))
  const openTask = (task: ScheduleTask) => {
    if (!task.canEdit) return
    setDraft({
      id: task.id, title: task.title, category: task.category,
      startDate: task.startDate, endDate: task.endDate, priority: task.priority,
      assigneeId: task.userId, srId: task.srId ?? '', note: task.note ?? '',
    })
  }

  const step = (direction: number) =>
    setAnchor(view === 'week' ? addDays(anchor, direction * 7) : shiftMonths(anchor, direction))

  const summary = board?.summary
  const heading = view === 'week'
    ? `${weekDays(anchor)[0]} ~ ${weekDays(anchor)[6]}`
    : monthLabel(anchor)

  return <>
    <PageHeader title="업무 상황판"
      description="부서의 한 달을 한 화면에 두고, 끝난 일은 체크로 지웁니다."/>
    <div className={`board-shell${fullscreen ? ' board-fullscreen' : ''}`} ref={boardRef}>
      <div className="board-bar">
        <div className="board-move">
          <button className="board-step" onClick={() => step(-1)} aria-label="이전">‹</button>
          <strong className="board-heading">{heading}</strong>
          <button className="board-step" onClick={() => step(1)} aria-label="다음">›</button>
          <Button variant="ghost" onClick={() => setAnchor(todayLocal())}>오늘</Button>
        </div>
        <div className="board-views" role="tablist" aria-label="보기 방식">
          {views.map(item => <button key={item.id} role="tab" aria-selected={view === item.id}
            className={`board-view${view === item.id ? ' active' : ''}`}
            onClick={() => setView(item.id)}>{item.label}</button>)}
        </div>
        <div className="board-tools">
          <label className="board-toggle"><input type="checkbox" checked={scope === 'TEAM'}
            onChange={event => setScope(event.target.checked ? 'TEAM' : 'SELF')}/>부서 전체</label>
          <label className="board-toggle"><input type="checkbox" checked={hideDone}
            onChange={event => setHideDone(event.target.checked)}/>완료 숨기기</label>
          <Button variant="secondary" onClick={() => openDay(view === 'week' ? anchor : monthStart(anchor))}>일정 추가</Button>
          <Button variant="ghost" onClick={toggleFullscreen}>{fullscreen ? '전체화면 끄기' : '전체화면'}</Button>
        </div>
      </div>

      {summary && <div className="board-summary">
        <span><b>{summary.total}</b>건</span>
        <span className="done"><b>{summary.done}</b> 완료 · {doneRatio(board?.tasks ?? [])}%</span>
        <span className="today"><b>{summary.today}</b> 오늘</span>
        <span className="overdue"><b>{summary.overdue}</b> 지연</span>
        <span className="urgent"><b>{summary.urgent}</b> 긴급</span>
        <span className="people"><b>{summary.people}</b>명</span>
        <span className="board-legend">
          {priorityOrder.map(level => <em key={level} className={`pri-${level.toLowerCase()}`}>{priorityLabels[level]}</em>)}
        </span>
      </div>}

      {failed && <div className="edit-notice" role="alert">{failed}</div>}

      {/* Every view below is drawn only from a board that was read. A grid of
          empty squares, seven columns of "일정 없음" and "등록된 업무 일정이
          없습니다" all say the same thing about a department, and none of them
          is true of a read that failed. */}
      {board && view === 'month' && <div className="month-grid">
        {weekdayNames.map((name, index) => <div key={name}
          className={`month-head${index === 0 ? ' sunday' : index === 6 ? ' saturday' : ''}`}>{name}</div>)}
        {weeks.flat().map(day => {
          const outside = day.slice(0, 7) !== anchor.slice(0, 7)
          const dayTasks = tasksOnDay(tasks, day)
          return <div key={day} className={`month-cell${outside ? ' outside' : ''}${day === today ? ' is-today' : ''}`}
            onDoubleClick={() => openDay(day)}>
            <div className="month-date">
              <span>{Number(day.slice(8, 10))}</span>
              <button className="month-add" onClick={() => openDay(day)} aria-label={`${day} 일정 추가`}>＋</button>
            </div>
            <div className="month-tasks">
              {dayTasks.map(task => <TaskChip key={task.id} task={task} today={today}
                onToggle={setDone} onOpen={openTask} compact/>)}
            </div>
          </div>
        })}
      </div>}

      {board && view === 'week' && <div className="board-week">
        {weekDays(anchor).map(day => <div key={day}
          className={`board-day${day === today ? ' is-today' : ''}`}>
          <div className="board-day-head">
            <strong>{weekdayNames[new Date(`${day}T00:00:00`).getDay()]}</strong>
            <span>{day.slice(5).replace('-', '/')}</span>
            <button className="month-add" onClick={() => openDay(day)} aria-label={`${day} 일정 추가`}>＋</button>
          </div>
          <div className="board-day-tasks">
            {tasksOnDay(tasks, day).map(task => <TaskChip key={task.id} task={task} today={today}
              onToggle={setDone} onOpen={openTask} showOwner/>)}
            {tasksOnDay(tasks, day).length === 0 && <p className="board-day-empty">일정 없음</p>}
          </div>
        </div>)}
      </div>}

      {board && view === 'list' && (tasks.length ? <div className="table-wrap board-list">
        <table><thead><tr><th>기간</th><th>중요도</th><th>업무</th><th>담당자</th><th>비고</th><th>완료</th></tr></thead>
          <tbody>{[...tasks].sort((left, right) => left.startDate < right.startDate ? -1 : left.startDate > right.startDate ? 1 : 0)
            .map(task => <tr key={task.id} className={`state-${taskState(task, today)}`}>
              <td>{task.startDate === task.endDate ? task.startDate : `${task.startDate} ~ ${task.endDate}`}</td>
              <td><span className={`pri-dot pri-${task.priority.toLowerCase()}`}/>{priorityLabels[task.priority]}</td>
              <td><button className="link-button" onClick={() => openTask(task)} disabled={!task.canEdit}>{task.title}</button>
                {task.srId && <SRLink task={task}/>}</td>
              <td>{task.displayName}</td>
              <td className="muted">{task.category}{task.note ? ` · ${task.note}` : ''}</td>
              <td><input type="checkbox" checked={task.done} disabled={!task.canEdit}
                onChange={event => void setDone(task, event.target.checked)}
                aria-label={`${task.title} 완료`}/></td>
            </tr>)}</tbody></table>
      </div> : <Empty>이 기간에 등록된 업무 일정이 없습니다.</Empty>)}

      {board && view === 'people' && (tasks.length ? <div className="people-lanes">
        {byAssignee(tasks, session.user.id).map(lane => <div key={lane.userId} className="people-lane">
          <div className="people-head">
            <strong>{lane.name}</strong>
            <span className="muted">{lane.tasks.filter(task => task.done).length}/{lane.tasks.length} · {doneRatio(lane.tasks)}%</span>
            <div className="people-bar"><i style={{ width: `${doneRatio(lane.tasks)}%` }}/></div>
          </div>
          <div className="people-tasks">
            {lane.tasks.map(task => <TaskChip key={task.id} task={task} today={today}
              onToggle={setDone} onOpen={openTask} showDate/>)}
          </div>
        </div>)}
      </div> : <Empty>이 기간에 등록된 업무 일정이 없습니다.</Empty>)}
    </div>

    {draft && <ScheduleDialog draft={draft} members={members} session={session}
      onChange={setDraft} onClose={() => setDraft(undefined)} onSave={save} onDelete={remove} notify={notify}/>}
  </>
}

/** One line on the board: the checkbox, the colour, and what it says. */
function TaskChip({ task, today, onToggle, onOpen, compact, showOwner, showDate }: {
  task: ScheduleTask; today: string; compact?: boolean; showOwner?: boolean; showDate?: boolean
  onToggle: (task: ScheduleTask, done: boolean) => void
  onOpen: (task: ScheduleTask) => void
}) {
  const state = taskState(task, today)
  return <div className={`task-chip pri-${task.priority.toLowerCase()} state-${state}${compact ? ' compact' : ''}`}>
    <input type="checkbox" checked={task.done} disabled={!task.canEdit}
      onChange={event => onToggle(task, event.target.checked)}
      aria-label={`${task.title} 완료`}/>
    <button className="task-title" onClick={() => onOpen(task)} disabled={!task.canEdit} title={task.note || task.title}>
      {showDate && <em>{task.startDate.slice(5).replace('-', '/')}</em>}
      {task.title}
      {showOwner && <small>{task.displayName}</small>}
    </button>
    {task.srId && <SRLink task={task} short={compact || showOwner}/>}
  </div>
}

/**
 * The ITSM number, and the link out to it.
 *
 * The address comes from the server, resolved from the link template rather
 * than stored on the row — so a portal that moves takes every one of these with
 * it. Without a template configured there is no link, and the number is still
 * shown, because knowing the SR exists is most of the value.
 */
function SRLink({ task, short }: { task: ScheduleTask; short?: boolean }) {
  // A month cell is one line wide. The whole number there would take the room
  // the title needs, so the part that actually tells two requests apart — the
  // sequence after the dash — is shown and the full number stays in the tooltip
  // and in every roomier view.
  const id = task.srId ?? ''
  const text = short ? (id.split('-').pop() || id) : id
  if (!task.srUrl) return <span className="sr-tag" title={id}>{text}</span>
  return <a className="sr-tag" href={task.srUrl} target="_blank" rel="noopener noreferrer"
    title={id} onClick={event => event.stopPropagation()}>{text}</a>
}

function ScheduleDialog({ draft, members, session, onChange, onClose, onSave, onDelete, notify }: {
  draft: Draft
  members: { id: number; displayName: string; organizationName: string }[]
  session: SessionInfo
  onChange: (draft: Draft) => void
  onClose: () => void
  onSave: (draft: Draft) => void
  onDelete: (draft: Draft) => void
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [looking, setLooking] = useState(false)
  const label = session.itsmLabel || 'SR'

  // Typing the number and getting the title is the point of the ITSM link: the
  // person building the board has the number in front of them and should not be
  // retyping a title that already exists somewhere else.
  const lookup = async () => {
    const id = draft.srId.trim()
    if (!id) { notify(`${label} 번호를 입력하세요.`, 'error'); return }
    setLooking(true)
    try {
      const found = await api<ITSMLookup>(`/api/v1/itsm/lookup?id=${encodeURIComponent(id)}`)
      onChange({ ...draft, srId: found.id, title: found.title || draft.title })
      notify(`${label} ${found.id} 제목을 가져왔습니다.`)
    } catch (error) { notify(errorText(error, `${label} 정보를 가져오지 못했습니다.`), 'error') }
    finally { setLooking(false) }
  }

  const heading = draft.id ? '업무 일정 수정' : '업무 일정 추가'

  // The house dialog shape — header, a padded form, a footer — rather than
  // fields laid straight onto the panel. The first version put them there and
  // every control sat against the edge of the white box with nothing between
  // the rows: a form that is legible only because it is short.
  return <Modal onClose={onClose} label={heading} className="schedule-dialog">
    <header>
      <h2>{heading}</h2>
      <button onClick={onClose} aria-label="닫기">×</button>
    </header>

    {/* The ITSM box is set apart because it is a different act: fetching
        something from another system, before filling anything in by hand. */}
    {session.itsmEnabled && <div className="sr-lookup">
      <label>{label} 번호<input value={draft.srId} placeholder="SR2609-00001"
        onChange={event => onChange({ ...draft, srId: event.target.value })}
        onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); void lookup() } }}/></label>
      <Button variant="secondary" onClick={lookup} disabled={looking}>{looking ? '가져오는 중…' : '제목 가져오기'}</Button>
      <p className="sr-hint">번호를 넣고 <strong>제목 가져오기</strong>를 누르면 ITSM에서 제목을 채웁니다. 비워 두어도 됩니다.</p>
    </div>}

    <div className="modal-form schedule-form">
      <label className="wide">업무명<input value={draft.title} autoFocus placeholder="예: 월간 안전점검"
        onChange={event => onChange({ ...draft, title: event.target.value })}/></label>
      <label>시작일<input type="date" value={draft.startDate}
        onChange={event => onChange({
          ...draft, startDate: event.target.value,
          endDate: draft.endDate < event.target.value ? event.target.value : draft.endDate,
        })}/></label>
      <label>종료일<input type="date" value={draft.endDate} min={draft.startDate}
        onChange={event => onChange({ ...draft, endDate: event.target.value })}/></label>
      <label>
        {/* The colour is what the board is read by, so the form shows it while
            it is being chosen rather than only after the row is drawn. */}
        <span className="label-line">중요도<i className={`pri-dot pri-${draft.priority.toLowerCase()}`}/></span>
        <select value={draft.priority}
          onChange={event => onChange({ ...draft, priority: event.target.value as SchedulePriority })}>
          {priorityOrder.map(level => <option key={level} value={level}>{priorityLabels[level]}</option>)}
        </select>
      </label>
      <label>구분<input value={draft.category} placeholder="감사 · 회의 · 배포"
        onChange={event => onChange({ ...draft, category: event.target.value })}/></label>
      {members.length > 0 && <label className="wide">담당자<select value={draft.assigneeId || session.user.id}
        onChange={event => onChange({ ...draft, assigneeId: Number(event.target.value) })}>
        <option value={session.user.id}>{session.user.displayName} (본인)</option>
        {members.map(member => <option key={member.id} value={member.id}>
          {member.displayName}{member.organizationName ? ` · ${member.organizationName}` : ''}</option>)}
      </select></label>}
      <label className="wide">비고<textarea rows={3} value={draft.note} placeholder="상황판 줄 위에 마우스를 올리면 보이는 설명입니다."
        onChange={event => onChange({ ...draft, note: event.target.value })}/></label>
      <p className="form-hint wide">
        하루짜리 일정은 종료일을 시작일과 같게 두면 됩니다. 며칠에 걸친 일은 걸친 날짜 칸에 모두 나옵니다.
      </p>
    </div>

    <footer>
      {draft.id > 0 && <Button variant="danger" className="footer-left" onClick={() => onDelete(draft)}>삭제</Button>}
      <Button variant="secondary" onClick={onClose}>취소</Button>
      <Button onClick={() => onSave(draft)}>저장</Button>
    </footer>
  </Modal>
}
