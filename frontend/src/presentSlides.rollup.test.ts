import { describe, expect, it } from 'vitest'
import { meetingSlides, rollupSlides } from './presentSlides'
import type { MeetingView, Rollup, RollupItem } from './types'

// The ordering rules in this file are meeting outcomes, not cosmetics. A room
// that runs out of time hears whatever came first, so what comes first is a
// decision somebody stands behind.

function insights() {
  return {
    totalItems: 3, completedItems: 1, inProgressItems: 1, notStartedItems: 1,
    completionRate: 33.3, averageProgress: 50, progressGain: 10,
    continuingItems: 2, oneOffItems: 1, stalledItems: 0, noLandingItems: 0,
    missesPeriod: 0, carryoverItems: 0, issueItems: 1, persistentIssues: 0,
    askItems: 1, expectedWeeks: 4, reportedWeeks: 4, reportCoverage: 100,
    sourceReports: 4, sourceItems: 6, duplicatesCut: 3, mergedTitles: 1,
    dedupRate: 50,
  } as Rollup['insights']
}

function rollupItem(overrides: Partial<RollupItem> = {}): RollupItem {
  return {
    key: 'k', category: '운영', title: '평범한 업무', currentResult: '진행',
    nextPlan: '계속', issue: '', managementAsk: '', progress: 50,
    owners: ['담당자'], atRisk: false, stalled: false, completed: false,
    weekCount: 1, firstWeek: '2026-08-03', lastWeek: '2026-08-24',
    startProgress: 0, issueWeeks: 0, mergedTitles: [], carryover: false, duplicatesCut: 0,
    forecast: { kind: 'PROJECTED', overallPerWeek: 10, recentPerWeek: 10, basedOnWeeks: 2, note: '' },
    periodOutlook: { kind: 'NONE' },
    ...overrides,
  } as RollupItem
}

function rollup(items: RollupItem[], overrides: Partial<Rollup> = {}): Rollup {
  return {
    kind: 'MONTH', label: '2026년 8월', scopeLabel: '팀',
    start: '2026-08-03', end: '2026-08-30', summary: '기간 요약',
    insights: insights(), items, highlights: [], decisions: [],
    decisionTotal: 0, openDecisions: 0, weeks: [],
    ...overrides,
  } as Rollup
}

describe('rollupSlides', () => {
  it('조치가 필요한 업무를 먼저 놓는다', () => {
    const slides = rollupSlides(rollup([
      rollupItem({ key: 'a', title: '평범' }),
      rollupItem({ key: 'b', title: '정체됨', stalled: true }),
      rollupItem({ key: 'c', title: '위험', atRisk: true }),
    ]))
    const titles = slides.filter(slide => slide.kind === 'entry').map(slide => slide.title)
    expect(titles.slice(0, 2).sort()).toEqual(['위험', '정체됨'])
    expect(titles[titles.length - 1]).toBe('평범')
  })

  it('상위 조직 요청이 있으면 그것도 먼저 볼 업무다', () => {
    // v0.92 put the ask in both decks because it is what management has to act
    // on. The same reasoning orders the room's attention here.
    const slides = rollupSlides(rollup([
      rollupItem({ key: 'a', title: '평범' }),
      rollupItem({ key: 'b', title: '요청 있음', managementAsk: '예산 승인 필요' }),
    ]))
    const titles = slides.filter(slide => slide.kind === 'entry').map(slide => slide.title)
    expect(titles[0]).toBe('요청 있음')
  })

  it('공백뿐인 요청은 조치 대상이 아니다', () => {
    const slides = rollupSlides(rollup([
      rollupItem({ key: 'a', title: '먼저' }),
      rollupItem({ key: 'b', title: '공백 요청', managementAsk: '   ' }),
    ]))
    const section = slides.find(slide => slide.kind === 'section' && slide.title === '업무별 실적')
    expect(section?.subtitle).toBeUndefined()
  })

  it('먼저 볼 업무가 몇 건인지 말한다', () => {
    const slides = rollupSlides(rollup([
      rollupItem({ key: 'a', title: '위험', atRisk: true }),
      rollupItem({ key: 'b', title: '평범' }),
    ]))
    const section = slides.find(slide => slide.kind === 'section' && slide.title === '업무별 실적')
    expect(section?.subtitle).toContain('1건')
  })

  it('경영 인사이트가 업무 목록보다 앞선다', () => {
    const slides = rollupSlides(rollup([rollupItem()], {
      highlights: [{ severity: 'RISK', title: '위험 신호', detail: '근거' }] as Rollup['highlights'],
    }))
    const insightAt = slides.findIndex(slide => slide.title === '경영 인사이트')
    const itemsAt = slides.findIndex(slide => slide.title === '업무별 실적')
    expect(insightAt).toBeGreaterThan(-1)
    expect(insightAt).toBeLessThan(itemsAt)
  })

  it('후속 조치가 남은 결정을 먼저 놓는다', () => {
    // One already carried out is history; one still owing something is why the
    // briefing is happening.
    const slides = rollupSlides(rollup([rollupItem()], {
      decisions: [
        { id: 1, title: '완료된 결정', status: 'DONE', decidedBy: '갑', decidedOn: '2026-08-10' },
        { id: 2, title: '남은 결정', status: 'OPEN', decidedBy: '을', decidedOn: '2026-08-11' },
      ] as Rollup['decisions'],
      decisionTotal: 2, openDecisions: 1,
    }))
    const decisionEntries = slides
      .filter(slide => slide.kind === 'entry' && slide.eyebrow?.startsWith('결정'))
      .map(slide => slide.title)
    expect(decisionEntries[0]).toBe('남은 결정')
    const section = slides.find(slide => slide.title === '기간 내 결정')
    expect(section?.subtitle).toContain('1건')
  })
})

describe('meetingSlides', () => {
  it('표지는 존재하는 안건 수를, 구획은 보여 주는 수를 말한다', () => {
    // A deck that says 40건 for a heading holding 2,100 lies to the room.
    const view = {
      week: '2026-08-24', scope: 'TEAM', workItems: 12, people: 4,
      sections: [
        { title: '이번 주 변화', total: 2100, entries: [{ title: 'A' }, { title: 'B' }] },
        { title: '새 이슈', total: 1, entries: [{ title: 'C' }] },
      ],
    } as unknown as MeetingView
    const slides = meetingSlides(view)
    expect(slides[0].subtitle).toContain('2101건')
    const truncated = slides.find(slide => slide.title === '이번 주 변화')
    expect(truncated?.eyebrow).toBe('2100건 중 2건')
    const whole = slides.find(slide => slide.title === '새 이슈')
    expect(whole?.eyebrow).toBe('1건')
  })

  it('비어 있는 구획은 화면을 차지하지 않는다', () => {
    const view = {
      week: '2026-08-24', scope: 'SELF', workItems: 0, people: 1,
      sections: [{ title: '빈 구획', total: 0, entries: [] }],
    } as unknown as MeetingView
    expect(meetingSlides(view).some(slide => slide.title === '빈 구획')).toBe(false)
  })
})
