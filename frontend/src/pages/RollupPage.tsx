import { useEffect, useMemo, useState } from 'react'
import { errorText, api } from '../api'
import { Button, Card, Empty, PageHeader, Spinner } from '../components'
import PresentationMode from '../PresentationMode'
import { rollupSlides } from '../presentSlides'
import { CompositionBar, ProgressTrendChart, RankBars, TaskTimeline, WeeklyStateChart } from '../charts'
import { replaceRoute } from '../router'
import type { PeriodKind, Rollup, RollupItem, RollupScope, SessionInfo } from '../types'

const kinds: { key: PeriodKind; name: string }[] = [
  { key: 'MONTH', name: '월간' }, { key: 'QUARTER', name: '분기' },
  { key: 'HALF', name: '반기' }, { key: 'YEAR', name: '연간' },
]

/** Builds the selectable period tokens ending at the period containing today. */
function periodOptions(kind: PeriodKind, today: Date): { value: string; label: string }[] {
  const year = today.getFullYear(), month = today.getMonth() + 1
  const options: { value: string; label: string }[] = []
  if (kind === 'MONTH') {
    for (let offset = 0; offset < 18; offset++) {
      const cursor = new Date(year, month - 1 - offset, 1)
      const value = `${cursor.getFullYear()}-${String(cursor.getMonth() + 1).padStart(2, '0')}`
      options.push({ value, label: `${cursor.getFullYear()}년 ${cursor.getMonth() + 1}월` })
    }
  } else if (kind === 'QUARTER') {
    let cursorYear = year, cursorQuarter = Math.floor((month - 1) / 3) + 1
    for (let offset = 0; offset < 12; offset++) {
      options.push({ value: `${cursorYear}-Q${cursorQuarter}`, label: `${cursorYear}년 ${cursorQuarter}분기` })
      cursorQuarter -= 1
      if (cursorQuarter === 0) { cursorQuarter = 4; cursorYear -= 1 }
    }
  } else if (kind === 'HALF') {
    let cursorYear = year, cursorHalf = month > 6 ? 2 : 1
    for (let offset = 0; offset < 8; offset++) {
      options.push({ value: `${cursorYear}-H${cursorHalf}`, label: `${cursorYear}년 ${cursorHalf === 1 ? '상반기' : '하반기'}` })
      cursorHalf -= 1
      if (cursorHalf === 0) { cursorHalf = 2; cursorYear -= 1 }
    }
  } else {
    for (let offset = 0; offset < 6; offset++) options.push({ value: `${year - offset}`, label: `${year - offset}년` })
  }
  return options
}

const severityNames: Record<string, string> = { RISK: '위험', WATCH: '주의', GOOD: '양호', INFO: '참고' }
const filters = [
  { key: 'ALL', name: '전체' }, { key: 'RISK', name: '이슈 지속' }, { key: 'STALLED', name: '정체' },
  { key: 'CARRYOVER', name: '이월' }, { key: 'DONE', name: '완료' },
] as const
type FilterKey = typeof filters[number]['key']

export default function RollupPage({ session, route, notify }: {
  session: SessionInfo
  route?: Record<string, string>
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const today = useMemo(() => new Date(), [])
  // Quick navigation links straight to a period, so the route wins over defaults.
  const routeKind = kinds.some(item => item.key === route?.kind) ? route?.kind as PeriodKind : undefined
  const [kind, setKind] = useState<PeriodKind>(routeKind ?? 'MONTH')
  const [period, setPeriod] = useState(() => {
    const initialKind = routeKind ?? 'MONTH'
    const options = periodOptions(initialKind, new Date())
    return options.some(option => option.value === route?.period) ? route!.period : options[0].value
  })
  const [scope, setScope] = useState<RollupScope>(route?.scope === 'TEAM' ? 'TEAM' : 'SELF')
  const [rollup, setRollup] = useState<Rollup>()
  const [presenting, setPresenting] = useState(false)
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<FilterKey>('ALL')
  const [detail, setDetail] = useState<RollupItem>()

  const canTeam = session.user.role !== 'USER'
  const options = useMemo(() => periodOptions(kind, today), [kind, today])

  const changeKind = (next: PeriodKind) => { setKind(next); setPeriod(periodOptions(next, today)[0].value) }

  // Keep the address bar in step so the current view stays linkable.
  useEffect(() => { replaceRoute('rollup', { kind, period, scope }) }, [kind, period, scope])

  useEffect(() => {
    let stale = false
    setLoading(true)
    const query = `kind=${kind}&period=${encodeURIComponent(period)}&scope=${scope}`
    api<Rollup>(`/api/v1/rollups?${query}`)
      .then(value => { if (!stale) setRollup(value) })
      .catch(error => { if (!stale) { setRollup(undefined); notify(errorText(error, '기간 보고를 불러올 수 없습니다.'), 'error') } })
      .finally(() => { if (!stale) setLoading(false) })
    return () => { stale = true }
  }, [kind, period, scope])

  const query = `kind=${kind}&period=${encodeURIComponent(period)}&scope=${scope}`
  const insights = rollup?.insights
  const visibleItems = (rollup?.items ?? []).filter(item => {
    switch (filter) {
      case 'RISK': return item.atRisk
      case 'STALLED': return item.stalled
      case 'CARRYOVER': return item.carryover
      case 'DONE': return item.completed
      default: return true
    }
  })

  return <>
    <PageHeader title="기간 업무보고"
      description="주간보고를 월간·분기·반기·연간 단위로 자동 취합하고 중복 업무와 반복 기재를 제거해 보여줍니다."
      action={<div className="header-actions">
        <Button variant="secondary" disabled={!rollup?.items.length} onClick={() => setPresenting(true)}>▶ 발표 모드</Button>
        <a className="button secondary" href={`/api/v1/rollups/export.csv?${query}`}>CSV</a>
        <a className="button primary" href={`/api/v1/rollups/export.pptx?${query}`}>PPTX 내보내기</a>
      </div>} />

    <div className="rollup-controls">
      <div className="tabs rollup-tabs">
        {kinds.map(item => <button key={item.key} className={kind === item.key ? 'active' : ''} onClick={() => changeKind(item.key)}>{item.name}</button>)}
      </div>
      <label>기간<select value={period} onChange={event => setPeriod(event.target.value)}>
        {options.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select></label>
      {canTeam && <label>범위<select value={scope} onChange={event => setScope(event.target.value as RollupScope)}>
        <option value="SELF">본인</option><option value="TEAM">소속 조직</option>
      </select></label>}
    </div>

    {loading || !rollup || !insights ? <Spinner /> : <>
      <Card className="rollup-summary">
        <div className="rollup-summary-head">
          <div>
            <span className="week-label">{rollup.label} · {rollup.scopeLabel}</span>
            <h3>{rollup.start} ~ {rollup.end}</h3>
          </div>
          <span className="mcp-badge">MCP 제공</span>
        </div>
        <p>{rollup.summary}</p>
      </Card>

      {insights.totalItems === 0 ? <Empty>이 기간에 취합할 주간보고가 없습니다. 주간보고를 등록하면 자동으로 집계됩니다.</Empty> : <>
        <div className="metric-grid">
          <Card><span className="metric-label">완료율</span><strong className="metric-value">{insights.completionRate.toFixed(1)}%</strong><small>{insights.completedItems} / {insights.totalItems}건 완료</small></Card>
          <Card><span className="metric-label">평균 진척도</span><strong className="metric-value">{insights.averageProgress.toFixed(1)}%</strong><small>기간 중 {insights.progressGain >= 0 ? '+' : ''}{insights.progressGain.toFixed(1)}%p 상승</small></Card>
          <Card><span className="metric-label">이슈 업무</span><strong className="metric-value">{insights.issueItems}</strong><small>{insights.persistentIssues}건은 이슈가 지속 중</small></Card>
          <Card><span className="metric-label">보고 커버리지</span><strong className="metric-value">{insights.reportCoverage.toFixed(0)}%</strong><small>{insights.expectedWeeks}개 주차 중 {insights.reportedWeeks}개 보고</small></Card>
        </div>

        <Card title="경영 인사이트" action={<span className="muted-chip">부서 보고 관점 자동 요약</span>}>
          <div className="insight-grid">
            {rollup.highlights.map((highlight, index) => <article key={index} className={`insight-card ${highlight.severity.toLowerCase()}`}>
              <header><span className="insight-tag">{severityNames[highlight.severity] ?? highlight.severity}</span><strong>{highlight.title}</strong></header>
              <p>{highlight.detail}</p>
            </article>)}
          </div>
        </Card>

        <div className="chart-duo">
          <Card title="주차별 업무 상태" className="chart-card">
            <WeeklyStateChart data={rollup.trend.map(point => ({
              weekStart: point.weekStart,
              completed: point.completedItems,
              progress: Math.max(0, point.activeItems - point.completedItems - point.notStartedItems),
              notStarted: point.notStartedItems,
            }))} />
          </Card>
          <Card title="주차별 평균 진척도" className="chart-card">
            <ProgressTrendChart data={rollup.trend} />
          </Card>
        </div>

        <Card title="업무 타임라인" action={<span className="muted-chip">중복 제거 후 {rollup.items.length}건</span>} className="chart-card">
          <TaskTimeline weeks={rollup.weeks} tasks={rollup.items.slice(0, 18)} />
          {rollup.items.length > 18 && <p className="muted">진척이 필요한 상위 18건만 표시합니다. 전체 {rollup.items.length}건은 아래 표와 CSV에서 확인하십시오.</p>}
        </Card>

        <div className="chart-duo">
          <Card title="업무 포트폴리오 구성" className="chart-card">
            <CompositionBar items={rollup.categories.map(category => ({ name: category.name, value: category.items }))} />
          </Card>
          <Card title={scope === 'TEAM' ? '구성원별 업무량' : '구분별 평균 진척도'} className="chart-card">
            {scope === 'TEAM'
              ? <RankBars items={rollup.contributors.slice(0, 8).map(contributor => ({
                  name: contributor.displayName, value: contributor.items,
                  note: `완료 ${contributor.completed}건 · 평균 ${contributor.averageProgress.toFixed(0)}%`,
                }))} />
              : <RankBars unit="%" items={rollup.categories.slice(0, 8).map(category => ({
                  name: category.name, value: Math.round(category.averageProgress),
                  note: `업무 ${category.items}건 · 완료 ${category.completed}건`,
                }))} />}
          </Card>
        </div>

        <Card title="취합 업무" action={<div className="tabs rollup-filter">
          {filters.map(item => <button key={item.key} className={filter === item.key ? 'active' : ''} onClick={() => setFilter(item.key)}>{item.name}</button>)}
        </div>}>
          {!visibleItems.length ? <Empty>선택한 조건에 해당하는 업무가 없습니다.</Empty> : <div className="table-wrap"><table>
            <thead><tr><th>구분</th><th>업무</th><th>진척도</th><th>수행 주차</th><th>상태</th><th>담당</th></tr></thead>
            <tbody>{visibleItems.map(item => <tr key={item.key} onClick={() => setDetail(item)}>
              <td>{item.category || '미분류'}</td>
              <td><strong>{item.title}</strong>{item.mergedTitles.length > 1 && <small className="cell-sub">동일 업무 {item.mergedTitles.length}건 병합</small>}</td>
              <td><div className="cell-progress"><i style={{ width: `${item.progress}%` }} /></div><small>{item.progress}%</small></td>
              <td>{item.weekCount}주<small className="cell-sub">{item.firstWeek} ~ {item.lastWeek}</small></td>
              <td><div className="state-chips">
                {item.completed && <span className="state-chip done">완료</span>}
                {item.atRisk
                  ? <span className="state-chip risk">이슈 {item.issueWeeks}주</span>
                  : item.issueWeeks >= 2 && <span className="state-chip past">이슈 이력 {item.issueWeeks}주</span>}
                {item.stalled && <span className="state-chip stalled">정체</span>}
                {item.carryover && !item.atRisk && !item.stalled && <span className="state-chip carry">이월</span>}
              </div></td>
              <td>{item.owners.join(', ') || '-'}</td>
            </tr>)}</tbody>
          </table></div>}
        </Card>
      </>}
    </>}

    {detail && <div className="modal-backdrop" onClick={() => setDetail(undefined)}>
      <div className="modal wide" onClick={event => event.stopPropagation()}>
        <header>
          <div>
            <span className="week-label">{detail.category || '미분류'} · {detail.firstWeek} ~ {detail.lastWeek} · {detail.weekCount}개 주차</span>
            <h2>{detail.title}</h2>
          </div>
          <button onClick={() => setDetail(undefined)}>×</button>
        </header>
        <div className="rollup-detail">
          <div className="rollup-detail-meta">
            <div><small>진척도</small><strong>{detail.startProgress}% → {detail.progress}%</strong></div>
            <div><small>담당</small><strong>{detail.owners.join(', ') || '-'}</strong></div>
            <div><small>이슈 발생</small><strong>{detail.issueWeeks}개 주차</strong></div>
            <div><small>중복 제거</small><strong>{detail.duplicatesCut}건</strong></div>
          </div>
          {detail.mergedTitles.length > 1 && <div className="rollup-merged">
            <strong>병합된 주간보고 업무명</strong>
            <ul>{detail.mergedTitles.map(title => <li key={title}>{title}</li>)}</ul>
          </div>}
          <section><b>기간 실적</b><p>{detail.currentResult || '-'}</p></section>
          <section><b>남은 계획</b><p>{detail.nextPlan || '-'}</p></section>
          <section><b>이슈</b><p>{detail.issue || '-'}</p></section>
        </div>
      </div>
    </div>}
    {presenting && rollup && <PresentationMode label={`${rollup.label} · ${rollup.scopeLabel}`}
      slides={rollupSlides(rollup)} onClose={() => setPresenting(false)} />}
  </>
}
