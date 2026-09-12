/**
 * The month a department reads off the wall, worked out as data.
 *
 * All of it is here rather than in the board component for the usual reason:
 * the grid is where the off-by-one lives. A month that starts on a Sunday, a
 * task that runs from the 28th of one month into the 3rd of the next, a
 * six-row February — these are answered once, in a module a test can ask
 * directly, instead of being re-derived inside a render that only a person
 * looking at the screen can check.
 *
 * Dates are plain yyyy-mm-dd throughout and are parsed at local midnight, for
 * the reason localdate.ts explains: east of Greenwich, `toISOString()` is
 * yesterday.
 */
import { localDate } from './localdate'
import type { ScheduleBoard, ScheduleTask, SchedulePriority } from './types'

/** The four levels, most urgent first. The colours are in the stylesheet. */
export const priorityOrder: SchedulePriority[] = ['URGENT', 'IMPORTANT', 'NEEDED', 'NORMAL']

export const priorityLabels: Record<SchedulePriority, string> = {
  URGENT: '긴급', IMPORTANT: '중요', NEEDED: '필요', NORMAL: '일반',
}

export function parseDay(day: string): Date {
  return new Date(`${day}T00:00:00`)
}

export function addDays(day: string, count: number): string {
  const date = parseDay(day)
  date.setDate(date.getDate() + count)
  return localDate(date)
}

/** The first of the month a yyyy-mm-dd falls in. */
export function monthStart(day: string): string {
  return `${day.slice(0, 7)}-01`
}

export function shiftMonths(day: string, months: number): string {
  const date = parseDay(monthStart(day))
  date.setMonth(date.getMonth() + months)
  return localDate(date)
}

/** "2026년 9월", for the heading. */
export function monthLabel(day: string): string {
  const date = parseDay(day)
  return `${date.getFullYear()}년 ${date.getMonth() + 1}월`
}

export type WeekStart = 0 | 1

/**
 * The days a month grid actually draws: whole weeks, so the first row starts on
 * the chosen weekday and the last row ends on one.
 *
 * The leading and trailing days belong to the neighbouring months and are drawn
 * greyed — but they are drawn, and work that falls on them has to be fetched,
 * which is why gridRange rather than the month itself is what the board asks
 * the server for.
 */
export function monthGrid(day: string, weekStartsOn: WeekStart = 0): string[][] {
  const first = parseDay(monthStart(day))
  const lead = (first.getDay() - weekStartsOn + 7) % 7
  const lastOfMonth = localDate(new Date(first.getFullYear(), first.getMonth() + 1, 0))

  const weeks: string[][] = []
  let cursor = addDays(localDate(first), -lead)
  // As many rows as the month needs and no more. A five-row month drawn on six
  // rows leaves an empty band across the bottom, which on a wall display is the
  // difference between a full screen and a wasted one.
  while (weeks.length < 6) {
    const week = Array.from({ length: 7 }, (_, index) => addDays(cursor, index))
    weeks.push(week)
    cursor = addDays(cursor, 7)
    if (week[6] >= lastOfMonth) break
  }
  return weeks
}

/** The first and last day the grid draws, which is what the board fetches. */
export function gridRange(weeks: string[][]): { from: string; to: string } {
  const first = weeks[0]?.[0] ?? ''
  const last = weeks[weeks.length - 1]?.[6] ?? first
  return { from: first, to: last }
}

/** The week containing a day, as seven dates. */
export function weekDays(day: string, weekStartsOn: WeekStart = 0): string[] {
  const lead = (parseDay(day).getDay() - weekStartsOn + 7) % 7
  const start = addDays(day, -lead)
  return Array.from({ length: 7 }, (_, index) => addDays(start, index))
}

export function covers(task: ScheduleTask, day: string): boolean {
  return task.startDate <= day && day <= task.endDate
}

/**
 * The lines on one day, in the order a board is read: 긴급 first, then the work
 * that started earliest, then by title so two runs of the same data draw the
 * same square.
 */
export function tasksOnDay(tasks: ScheduleTask[], day: string): ScheduleTask[] {
  return tasks.filter(task => covers(task, day)).sort(compareTasks)
}

export function compareTasks(left: ScheduleTask, right: ScheduleTask): number {
  const rank = priorityOrder.indexOf(left.priority) - priorityOrder.indexOf(right.priority)
  if (rank !== 0) return rank
  if (left.startDate !== right.startDate) return left.startDate < right.startDate ? -1 : 1
  if (left.title !== right.title) return left.title < right.title ? -1 : 1
  return left.id - right.id
}

export type TaskState = 'done' | 'overdue' | 'today' | 'planned'

/**
 * What a line is, which decides how it is drawn.
 *
 * Done wins over overdue on purpose: work finished late is finished, and a
 * board that keeps it red says the department still owes it.
 */
export function taskState(task: ScheduleTask, today: string): TaskState {
  if (task.done) return 'done'
  if (task.endDate < today) return 'overdue'
  if (task.startDate <= today && today <= task.endDate) return 'today'
  return 'planned'
}

/** Lanes for the by-assignee view, ordered by name with the reader first. */
export function byAssignee(tasks: ScheduleTask[], meId?: number): { userId: number; name: string; tasks: ScheduleTask[] }[] {
  const lanes = new Map<number, { userId: number; name: string; tasks: ScheduleTask[] }>()
  for (const task of tasks) {
    const lane = lanes.get(task.userId) ?? { userId: task.userId, name: task.displayName, tasks: [] }
    lane.tasks.push(task)
    lanes.set(task.userId, lane)
  }
  return [...lanes.values()]
    .map(lane => ({ ...lane, tasks: [...lane.tasks].sort(compareTasks) }))
    .sort((left, right) => {
      if (left.userId === meId) return -1
      if (right.userId === meId) return 1
      return left.name.localeCompare(right.name, 'ko')
    })
}

/**
 * What a failed read is allowed to leave on the screen.
 *
 * 전체화면 hangs this board on a wall and re-reads it every minute, so a minute
 * that fails must not wipe it: the last good answer for the same window is
 * still the department's plan, and a blank wall is worse than one that is a
 * minute old. The banner says the read failed; the rows stay.
 *
 * A board for a different window is not old, it is wrong. Stepping to October
 * and failing would draw September's rows under October's heading, and turning
 * 부서 전체 off and failing would draw the department's rows beside a toggle
 * that says they are the reader's own. Neither is stale data — both are the
 * screen answering a question nobody asked. Those are dropped, and the board
 * then draws nothing at all, because an empty month is a claim too: "이 부서는
 * 이번 달에 아무 계획이 없습니다" is a different sentence from "계획을 읽지
 * 못했습니다".
 */
export function boardAfterFailure(
  board: ScheduleBoard | undefined, from: string, to: string, scope: 'SELF' | 'TEAM',
): ScheduleBoard | undefined {
  if (!board) return undefined
  return board.from === from && board.to === to && board.scope === scope ? board : undefined
}

/**
 * 화면이 이번 달을 다 그리지 못했을 때 할 말.
 *
 * 서버는 한 번에 2,000줄까지 보냅니다 — 300명 부서의 한 달이 900줄이고, 기간은
 * 한 해까지 넓힐 수 있기 때문입니다. 잘린 것을 말하지 않으면 벽에 걸린 판은
 * 남은 일이 없다고 말하는 셈이고, 그것은 판이 있는 이유를 정확히 뒤집습니다.
 *
 * 위쪽 요약(지연·오늘·긴급)은 잘리기 전 전체를 센 것이라 문장과 숫자가 어긋나지
 * 않습니다. 그래서 여기서는 "무엇을 하면 다 볼 수 있는지" 만 말합니다.
 */
export function boardTruncation(board: ScheduleBoard | undefined): string {
  if (!board || typeof board.total !== 'number') return ''
  if (board.total <= board.tasks.length) return ''
  return `이 기간에 ${board.total.toLocaleString()}건이 있고 ${board.tasks.length.toLocaleString()}건만 그렸습니다. `
    + '위 집계는 전체를 센 것입니다. 기간을 좁히거나 범위를 내 일정으로 바꾸면 전부 보입니다.'
}

/**
 * 편집 창이 들고 있는 한 줄.
 *
 * 저장은 PUT 이고 PUT 은 줄 전체를 보낸 것으로 갈아 끼웁니다 — 그래서 창이 들고
 * 있지 않은 것은 저장하는 순간 사라집니다. 화면에 아무 데도 그려지지 않는
 * workItemId 가 여기 있는 이유가 그것입니다: 업무 추적으로 이어지는 링크는
 * 상황판의 어느 보기에도 나타나지 않으므로, 제목의 오타 하나를 고치는 사람은
 * 그런 것이 있다는 사실조차 모르는 채로 계획과 그 계획이 가리키던 업무를
 * 끊게 됩니다. 창이 값을 들고 다니면 고치지 않은 것은 고쳐지지 않습니다.
 */
export interface ScheduleDraft {
  id: number; title: string; category: string
  startDate: string; endDate: string
  priority: SchedulePriority; assigneeId: number; srId: string; note: string
  workItemId?: number
}

/** 그 날짜에 새로 쓰는 빈 줄. */
export function emptyDraft(day: string): ScheduleDraft {
  return {
    id: 0, title: '', category: '', startDate: day, endDate: day,
    priority: 'NORMAL', assigneeId: 0, srId: '', note: '',
  }
}

/** 판에 있는 줄을 편집 창이 들 수 있는 모양으로 — 보이지 않는 것까지 그대로. */
export function draftOf(task: ScheduleTask): ScheduleDraft {
  return {
    id: task.id, title: task.title, category: task.category,
    startDate: task.startDate, endDate: task.endDate, priority: task.priority,
    assigneeId: task.userId, srId: task.srId ?? '', note: task.note ?? '',
    workItemId: task.workItemId,
  }
}

/** 저장이 보내는 것. 창이 든 것을 전부 보냅니다 — 빠뜨린 것은 지워집니다. */
export function scheduleBody(draft: ScheduleDraft) {
  return {
    title: draft.title.trim(), category: draft.category.trim(),
    startDate: draft.startDate, endDate: draft.endDate || draft.startDate,
    priority: draft.priority, note: draft.note.trim(),
    srId: draft.srId.trim(), assigneeId: draft.assigneeId || undefined,
    workItemId: draft.workItemId,
  }
}

/**
 * 담당자를 바꾸면 업무 링크는 함께 내려놓습니다.
 *
 * 한 줄의 링크는 담당자 본인의 업무만 가리킬 수 있습니다 — 상황판과 업무
 * 추적이 같은 일을 말하게 하려고 서버가 그렇게 거절합니다. 그러므로 일을 다른
 * 사람에게 넘기면서 링크를 함께 보내는 것은 저장 자체가 실패하는 길이고, 그
 * 실패는 화면에 보이지도 않는 값 때문에 일어납니다. 넘길 때 링크를 놓는 쪽이
 * 남은 하나입니다.
 */
export function withAssignee(draft: ScheduleDraft, assigneeId: number): ScheduleDraft {
  if (assigneeId === draft.assigneeId) return draft
  return { ...draft, assigneeId, workItemId: undefined }
}

/** How many of a set are finished, for the progress strip. */
export function doneRatio(tasks: ScheduleTask[]): number {
  return donePercent(tasks.filter(task => task.done).length, tasks.length)
}

/**
 * 완료율을 이미 세어 둔 수로 구합니다.
 *
 * 위쪽 요약은 잘리기 전 전체를 센 것이고 그려진 줄은 그 일부일 수 있으므로,
 * 같은 줄에 있는 "N 완료" 와 "M%" 가 서로 다른 모집단을 말하면 안 됩니다.
 */
export function donePercent(done: number, total: number): number {
  if (total <= 0) return 0
  return Math.round((done / total) * 100)
}

export const weekdayNames = ['일', '월', '화', '수', '목', '금', '토']
