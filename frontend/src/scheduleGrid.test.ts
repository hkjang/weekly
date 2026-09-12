import { describe, expect, it } from 'vitest'
import {
  addDays, boardAfterFailure, boardTruncation, byAssignee, chipsForDay, compareTasks, doneRatio, donePercent, draftOf, emptyDraft, gridRange, movedDates, readBoardPreference,
  monthGrid, monthLabel, monthStart, scheduleBody, shiftMonths, taskState, tasksOnDay, weekDays, writeBoardPreference,
  withAssignee,
} from './scheduleGrid'
import type { ScheduleBoard, ScheduleTask } from './types'

function task(partial: Partial<ScheduleTask> & { id: number; startDate: string }): ScheduleTask {
  return {
    id: partial.id, title: partial.title ?? `업무 ${partial.id}`, category: partial.category ?? '',
    startDate: partial.startDate, endDate: partial.endDate ?? partial.startDate,
    done: partial.done ?? false, userId: partial.userId ?? 1,
    displayName: partial.displayName ?? '사용자', createdById: partial.createdById ?? 1,
    priority: partial.priority ?? 'NORMAL', canEdit: partial.canEdit ?? true,
    srId: partial.srId, srUrl: partial.srUrl, doneByName: partial.doneByName,
    workItemId: partial.workItemId, note: partial.note,
  }
}

describe('월 격자', () => {
  it('주 단위로 떨어지는 격자를 만든다', () => {
    // 2026-09-01 is a Tuesday, so a Sunday-start grid begins on 8/30.
    const weeks = monthGrid('2026-09-14')
    expect(weeks[0][0]).toBe('2026-08-30')
    expect(weeks.every(week => week.length === 7)).toBe(true)
    expect(weeks[weeks.length - 1][6] >= '2026-09-30').toBe(true)
    // Every date is one day after the last.
    const flat = weeks.flat()
    for (let index = 1; index < flat.length; index++) {
      expect(flat[index]).toBe(addDays(flat[index - 1], 1))
    }
  })

  it('주 시작 요일을 월요일로 바꾸면 격자도 움직인다', () => {
    expect(monthGrid('2026-09-14', 1)[0][0]).toBe('2026-08-31')
  })

  it('필요한 만큼만 줄을 그린다', () => {
    // A 28 day February that starts on the week's first day fits in four rows;
    // a month that spills past the sixth week never gets a seventh.
    expect(monthGrid('2027-02-10', 1).length).toBe(4)
    for (const month of ['2026-01-01', '2026-05-01', '2026-08-01', '2027-08-01']) {
      const weeks = monthGrid(month)
      expect(weeks.length).toBeGreaterThanOrEqual(4)
      expect(weeks.length).toBeLessThanOrEqual(6)
    }
  })

  it('격자가 그리는 범위를 그대로 조회 범위로 준다', () => {
    const weeks = monthGrid('2026-09-14')
    expect(gridRange(weeks)).toEqual({ from: '2026-08-30', to: weeks[weeks.length - 1][6] })
  })

  it('월 이동과 이름', () => {
    expect(monthStart('2026-09-14')).toBe('2026-09-01')
    expect(shiftMonths('2026-01-31', 1)).toBe('2026-02-01')
    expect(shiftMonths('2026-01-15', -1)).toBe('2025-12-01')
    expect(monthLabel('2026-09-14')).toBe('2026년 9월')
  })

  it('주 보기는 그 날이 속한 7일을 준다', () => {
    expect(weekDays('2026-09-09')).toEqual([
      '2026-09-06', '2026-09-07', '2026-09-08', '2026-09-09', '2026-09-10', '2026-09-11', '2026-09-12',
    ])
    expect(weekDays('2026-09-09', 1)[0]).toBe('2026-09-07')
  })
})

describe('하루에 걸리는 일정', () => {
  const spanning = task({ id: 1, startDate: '2026-08-28', endDate: '2026-09-03', title: '감사 준비' })
  const single = task({ id: 2, startDate: '2026-09-01', title: '주간회의', priority: 'URGENT' })

  it('여러 날에 걸친 일정은 걸친 모든 날에 나온다', () => {
    for (const day of ['2026-08-28', '2026-08-31', '2026-09-03']) {
      expect(tasksOnDay([spanning], day).map(item => item.id)).toEqual([1])
    }
    expect(tasksOnDay([spanning], '2026-09-04')).toEqual([])
    expect(tasksOnDay([spanning], '2026-08-27')).toEqual([])
  })

  it('긴급이 먼저 온다', () => {
    expect(tasksOnDay([spanning, single], '2026-09-01').map(item => item.id)).toEqual([2, 1])
  })

  it('같은 중요도는 시작일, 제목, id 순으로 안정적으로 정렬된다', () => {
    const early = task({ id: 9, startDate: '2026-09-01', title: 'ㄴ' })
    const late = task({ id: 3, startDate: '2026-09-02', title: 'ㄱ' })
    expect([late, early].sort(compareTasks).map(item => item.id)).toEqual([9, 3])
  })
})

describe('상태', () => {
  const today = '2026-09-14'
  it('끝난 일은 늦었어도 끝난 일이다', () => {
    expect(taskState(task({ id: 1, startDate: '2026-09-01', done: true }), today)).toBe('done')
  })
  it('지난 일, 오늘 일, 앞으로의 일', () => {
    expect(taskState(task({ id: 2, startDate: '2026-09-10' }), today)).toBe('overdue')
    expect(taskState(task({ id: 3, startDate: '2026-09-12', endDate: '2026-09-16' }), today)).toBe('today')
    expect(taskState(task({ id: 4, startDate: '2026-09-20' }), today)).toBe('planned')
  })
})

describe('담당자별 보기', () => {
  it('보는 사람을 맨 위에 두고 나머지는 이름순으로 묶는다', () => {
    const tasks = [
      task({ id: 1, startDate: '2026-09-01', userId: 7, displayName: '홍길동' }),
      task({ id: 2, startDate: '2026-09-02', userId: 3, displayName: '김철수' }),
      task({ id: 3, startDate: '2026-09-03', userId: 7, displayName: '홍길동' }),
    ]
    const lanes = byAssignee(tasks, 7)
    expect(lanes.map(lane => lane.userId)).toEqual([7, 3])
    expect(lanes[0].tasks.map(item => item.id)).toEqual([1, 3])
  })

  it('완료율', () => {
    expect(doneRatio([])).toBe(0)
    expect(doneRatio([task({ id: 1, startDate: '2026-09-01', done: true }), task({ id: 2, startDate: '2026-09-02' })])).toBe(50)
  })
})

/**
 * 저장은 줄 전체를 갈아 끼우므로, 편집 창이 들지 않은 것은 저장하는 순간
 * 사라집니다. 업무 링크는 상황판 어디에도 그려지지 않아 고치는 사람이 그것이
 * 있다는 것조차 알 수 없으므로, 제목의 오타 하나가 계획과 업무의 연결을 조용히
 * 끊는 자리였습니다.
 */
describe('편집 창이 들고 가는 것', () => {
  const linked = task({ id: 5, startDate: '2026-09-08', endDate: '2026-09-09', userId: 7 })

  it('보이지 않는 업무 링크까지 창이 들고, 저장이 그대로 보낸다', () => {
    const draft = draftOf({ ...linked, workItemId: 42, note: '회의실 2층', srId: 'SR2609-00001' })
    expect(draft.workItemId).toBe(42)
    expect(scheduleBody(draft).workItemId).toBe(42)
    // 제목만 고쳐도 나머지는 그대로 실려 갑니다.
    expect(scheduleBody({ ...draft, title: '감사 사전 점검(정정)' })).toMatchObject({
      title: '감사 사전 점검(정정)', endDate: '2026-09-09', workItemId: 42,
      note: '회의실 2층', srId: 'SR2609-00001', assigneeId: 7,
    })
  })

  it('링크가 없던 줄은 없는 채로 간다', () => {
    expect(scheduleBody(draftOf(linked)).workItemId).toBeUndefined()
    expect(scheduleBody(emptyDraft('2026-09-08'))).toMatchObject({
      startDate: '2026-09-08', endDate: '2026-09-08', assigneeId: undefined, workItemId: undefined,
    })
  })

  it('담당자를 바꾸면 업무 링크는 함께 내려놓는다', () => {
    // 링크는 담당자 본인의 업무만 가리킬 수 있어 서버가 거절합니다 — 보이지도
    // 않는 값 때문에 저장이 실패하는 것보다 넘길 때 놓는 쪽이 낫습니다.
    const draft = draftOf({ ...linked, workItemId: 42 })
    expect(withAssignee(draft, 9).workItemId).toBeUndefined()
    expect(withAssignee(draft, 9).assigneeId).toBe(9)
    // 같은 사람을 다시 고르는 것은 바꾼 것이 아닙니다.
    expect(withAssignee(draft, 7).workItemId).toBe(42)
  })
})

describe('다 그리지 못한 달', () => {
  const board = (total: number, drawn: number): ScheduleBoard => ({
    from: '2026-09-01', to: '2026-09-30', scope: 'TEAM', today: '2026-09-11',
    tasks: Array.from({ length: drawn }, (_, index) => task({ id: index + 1, startDate: '2026-09-11' })),
    total,
    summary: { total, done: 0, overdue: 0, today: 0, people: 1, urgent: 0 },
  })

  it('다 그렸으면 아무 말도 하지 않는다', () => {
    expect(boardTruncation(board(3, 3))).toBe('')
    expect(boardTruncation(undefined)).toBe('')
  })

  it('잘렸으면 몇 건 중 몇 건인지와 무엇을 하면 되는지 말한다', () => {
    const notice = boardTruncation(board(2400, 2000))
    expect(notice).toContain('2,400')
    expect(notice).toContain('2,000')
    // 위 요약은 전체를 센 것이므로, 문장이 숫자를 부정하지 않아야 합니다.
    expect(notice).toContain('전체를 센 것')
  })
})

describe('완료율은 요약과 같은 모집단을 말한다', () => {
  it('그려진 줄이 아니라 세어 둔 수로 구한다', () => {
    // 2,400건 가운데 1,200건이 완료인데 2,000건만 그려진 판.
    expect(donePercent(1200, 2400)).toBe(50)
    expect(donePercent(0, 0)).toBe(0)
  })
})

describe('읽기가 실패했을 때 화면에 남는 것', () => {
  const board = (over: Partial<ScheduleBoard> = {}): ScheduleBoard => ({
    from: '2026-08-30', to: '2026-10-03', scope: 'TEAM', today: '2026-09-11',
    tasks: [task({ id: 1, startDate: '2026-09-11' })],
    total: 1,
    summary: { total: 1, done: 0, overdue: 0, today: 1, people: 1, urgent: 0 },
    ...over,
  })

  it('같은 창을 다시 읽다 실패하면 벽에 걸린 판을 지우지 않는다', () => {
    const kept = boardAfterFailure(board(), '2026-08-30', '2026-10-03', 'TEAM')
    expect(kept?.tasks.map(item => item.id)).toEqual([1])
  })

  it('다음 달로 넘어가다 실패하면 지난달 줄을 이번 달 제목 아래 남기지 않는다', () => {
    expect(boardAfterFailure(board(), '2026-09-27', '2026-10-31', 'TEAM')).toBeUndefined()
  })

  it('부서 전체를 끄다 실패하면 남의 줄을 본인 것으로 남기지 않는다', () => {
    expect(boardAfterFailure(board(), '2026-08-30', '2026-10-03', 'SELF')).toBeUndefined()
  })

  it('한 번도 읽지 못했으면 남길 것이 없다', () => {
    expect(boardAfterFailure(undefined, '2026-08-30', '2026-10-03', 'TEAM')).toBeUndefined()
  })
})

describe('바쁜 하루가 든 달력 칸', () => {
  const day = '2026-09-11'
  const many = (count: number) => Array.from({ length: count }, (_, index) => task({
    id: index + 1, startDate: day, endDate: day,
    priority: index === 9 ? 'URGENT' : 'NORMAL',
  }))

  it('한가한 날은 그대로 다 그린다', () => {
    const { shown, hidden } = chipsForDay(many(3), day)
    expect(shown).toHaveLength(3)
    expect(hidden).toBe(0)
  })

  it('넘치면 앞의 몇 줄만 그리고 나머지는 수로 말한다', () => {
    const { shown, hidden } = chipsForDay(many(20), day)
    expect(shown.length + hidden).toBe(20)
    expect(hidden).toBe(20 - shown.length)
    // 한 칸이 스무 줄이면 그 주의 행이 화면을 다 차지합니다. 남는 줄은 적어야
    // 달이 달로 보입니다.
    expect(shown.length).toBeLessThanOrEqual(6)
  })

  it('잘리는 쪽은 언제나 덜 급한 쪽이다', () => {
    // 열 번째가 긴급입니다 — 줄 수가 넘쳐도 그 줄은 남아야 합니다.
    const { shown } = chipsForDay(many(20), day)
    expect(shown.some(item => item.priority === 'URGENT')).toBe(true)
  })

  it('그 날에 걸치지 않는 줄은 세지 않는다', () => {
    const elsewhere = task({ id: 99, startDate: '2026-10-01', endDate: '2026-10-01' })
    const { shown, hidden } = chipsForDay([...many(2), elsewhere], day)
    expect(shown).toHaveLength(2)
    expect(hidden).toBe(0)
  })
})

describe('끝난 줄은 뒤로', () => {
  const day = '2026-09-11'
  it('남은 일이 지운 일보다 먼저 그려진다', () => {
    const finishedUrgent = task({ id: 1, startDate: day, endDate: day, priority: 'URGENT', done: true })
    const openNormal = task({ id: 2, startDate: day, endDate: day, priority: 'NORMAL' })
    expect([finishedUrgent, openNormal].sort(compareTasks).map(item => item.id)).toEqual([2, 1])
  })

  it('칸이 넘칠 때 끝난 줄이 남은 줄을 밀어내지 않는다', () => {
    const done = Array.from({ length: 8 }, (_, index) => task({
      id: index + 1, startDate: day, endDate: day, priority: 'URGENT', done: true,
    }))
    const open = task({ id: 99, startDate: day, endDate: day, priority: 'NORMAL' })
    const { shown } = chipsForDay([...done, open], day)
    expect(shown.map(item => item.id)).toContain(99)
  })
})

describe('줄을 다른 날로 옮기면', () => {
  it('하루짜리는 그 하루로 간다', () => {
    const one = task({ id: 1, startDate: '2026-09-07', endDate: '2026-09-07' })
    expect(movedDates(one, '2026-09-10')).toEqual({ startDate: '2026-09-10', endDate: '2026-09-10' })
  })

  it('여러 날짜리는 기간을 그대로 들고 간다', () => {
    // 9/7~9/11 은 닷새입니다. 9/9 에 놓으면 9/9~9/13 이지 9/9 하루가 아닙니다.
    const span = task({ id: 2, startDate: '2026-09-07', endDate: '2026-09-11' })
    expect(movedDates(span, '2026-09-09')).toEqual({ startDate: '2026-09-09', endDate: '2026-09-13' })
  })

  it('달을 건너뛰어도 기간이 유지된다', () => {
    const span = task({ id: 3, startDate: '2026-09-28', endDate: '2026-10-02' })
    expect(movedDates(span, '2026-12-30')).toEqual({ startDate: '2026-12-30', endDate: '2027-01-03' })
  })

  it('옮긴 줄은 옮기기 전 값을 하나도 잃지 않는다', () => {
    // PUT 은 줄 전체를 갈아 끼웁니다 — 끌어 놓기가 보내는 것도 창이 보내는 것과
    // 같아야 화면에 그려지지 않는 업무 링크가 조용히 끊기지 않습니다.
    const linked = task({ id: 4, startDate: '2026-09-07', endDate: '2026-09-08', workItemId: 42, srId: 'SR2609-00001', note: '비고' })
    const moved = { ...draftOf(linked), ...movedDates(linked, '2026-09-14') }
    const body = scheduleBody(moved)
    expect(body.workItemId).toBe(42)
    expect(body.srId).toBe('SR2609-00001')
    expect(body.note).toBe('비고')
    expect(body.startDate).toBe('2026-09-14')
    expect(body.endDate).toBe('2026-09-15')
  })
})

describe('화면이 기억하는 것', () => {
  it('저장소를 읽을 수 없으면 기본값으로 돌아간다', () => {
    // 사생활 모드처럼 localStorage 가 막힌 브라우저에서도 판은 열려야 합니다.
    expect(readBoardPreference('view', ['month', 'week'] as const, 'month')).toBe('month')
    expect(() => writeBoardPreference('view', 'week')).not.toThrow()
  })

  it('저장된 적 없는 값이나 모르는 값은 기본값이다', () => {
    const store: Record<string, string> = { 'weekly.board.view': '사람' }
    const original = (globalThis as { window?: unknown }).window
    ;(globalThis as { window?: unknown }).window = {
      localStorage: { getItem: (key: string) => store[key] ?? null, setItem: () => undefined },
    }
    try {
      expect(readBoardPreference('view', ['month', 'week'] as const, 'week')).toBe('week')
      store['weekly.board.view'] = 'month'
      expect(readBoardPreference('view', ['month', 'week'] as const, 'week')).toBe('month')
    } finally {
      ;(globalThis as { window?: unknown }).window = original
    }
  })
})
