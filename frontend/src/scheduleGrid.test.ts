import { describe, expect, it } from 'vitest'
import {
  addDays, boardAfterFailure, byAssignee, compareTasks, doneRatio, gridRange, monthGrid,
  monthLabel, monthStart, shiftMonths, taskState, tasksOnDay, weekDays,
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

describe('읽기가 실패했을 때 화면에 남는 것', () => {
  const board = (over: Partial<ScheduleBoard> = {}): ScheduleBoard => ({
    from: '2026-08-30', to: '2026-10-03', scope: 'TEAM', today: '2026-09-11',
    tasks: [task({ id: 1, startDate: '2026-09-11' })],
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
