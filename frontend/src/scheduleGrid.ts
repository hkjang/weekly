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

/** How many of a set are finished, for the progress strip. */
export function doneRatio(tasks: ScheduleTask[]): number {
  if (tasks.length === 0) return 0
  return Math.round((tasks.filter(task => task.done).length / tasks.length) * 100)
}

export const weekdayNames = ['일', '월', '화', '수', '목', '금', '토']
