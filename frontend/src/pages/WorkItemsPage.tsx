import { useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import type { SessionInfo, WorkItem } from '../types'

/**
 * Work item tracking. Each row is a task followed across every week it was
 * reported in, which is what makes ageing visible: how long it has run, how
 * many weeks went unreported, and how long it has sat at the same progress.
 */

const filters = [
  { key: 'ALL', name: '전체' },
  { key: 'ATTENTION', name: '조치 필요' },
  { key: 'STALLED', name: '정체' },
  { key: 'RISK', name: '이슈 지속' },
  { key: 'ASK', name: '요청 있음' },
  { key: 'ACTIVE', name: '진행 중' },
  { key: 'DONE', name: '완료' },
] as const
type FilterKey = typeof filters[number]['key']

export default function WorkItemsPage({ session, notify }: {
  session: SessionInfo
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [scope, setScope] = useState<'SELF' | 'TEAM'>('SELF')
  const [items, setItems] = useState<WorkItem[]>()
  const [filter, setFilter] = useState<FilterKey>('ALL')
  const [detail, setDetail] = useState<WorkItem>()
  const canTeam = session.user.role !== 'USER'

  useEffect(() => {
    let stale = false
    setItems(undefined)
    api<WorkItem[]>(`/api/v1/work-items?scope=${scope}`)
      .then(value => { if (!stale) setItems(value) })
      .catch(error => {
        if (stale) return
        setItems([])
        notify(error instanceof Error ? error.message : '업무를 불러올 수 없습니다.', 'error')
      })
    return () => { stale = true }
  }, [scope])

  const visible = useMemo(() => (items ?? []).filter(item => {
    switch (filter) {
      case 'ATTENTION': return item.stalled || item.atRisk || !!item.latestManagementAsk
      case 'STALLED': return item.stalled
      case 'RISK': return item.atRisk
      case 'ASK': return !!item.latestManagementAsk
      case 'ACTIVE': return !item.completed
      case 'DONE': return item.completed
      default: return true
    }
  }), [items, filter])

  const counts = useMemo(() => {
    const list = items ?? []
    return {
      total: list.length,
      active: list.filter(item => !item.completed).length,
      stalled: list.filter(item => item.stalled).length,
      risk: list.filter(item => item.atRisk).length,
      ask: list.filter(item => !!item.latestManagementAsk).length,
      oldest: list.reduce((max, item) => Math.max(max, item.ageWeeks), 0),
    }
  }, [items])

  return <>
    <PageHeader title="업무 추적"
      description="주차를 넘어 이어지는 업무를 하나의 흐름으로 따라갑니다. 얼마나 오래 진행됐고, 몇 주 보고가 빠졌고, 언제부터 진척이 멈췄는지 확인합니다."
      action={canTeam ? <label className="inline-select">범위
        <select value={scope} onChange={event => setScope(event.target.value as 'SELF' | 'TEAM')}>
          <option value="SELF">본인</option><option value="TEAM">소속 조직</option>
        </select></label> : undefined} />

    {items === undefined ? <Spinner /> : !items.length ? <Empty>
      아직 추적할 업무가 없습니다. 주간보고를 저장하면 업무가 자동으로 만들어집니다.
    </Empty> : <>
      <div className="metric-grid">
        <Card><span className="metric-label">진행 중 업무</span><strong className="metric-value">{counts.active}</strong><small>전체 {counts.total}건</small></Card>
        <Card><span className="metric-label">정체</span><strong className="metric-value">{counts.stalled}</strong><small>진척도 변화 없음</small></Card>
        <Card><span className="metric-label">이슈 지속</span><strong className="metric-value">{counts.risk}</strong><small>미완료 상태로 반복 보고</small></Card>
        <Card><span className="metric-label">최장 진행</span><strong className="metric-value">{counts.oldest}주</strong><small>가장 오래된 업무</small></Card>
      </div>

      <Card title="업무 목록" action={<div className="tabs rollup-filter">
        {filters.map(item => <button key={item.key} className={filter === item.key ? 'active' : ''}
          onClick={() => setFilter(item.key)}>{item.name}</button>)}
      </div>}>
        {!visible.length ? <Empty>조건에 맞는 업무가 없습니다.</Empty> : <div className="table-wrap"><table>
          <thead><tr>
            <th>구분</th><th>업무</th><th>진척도</th><th>경과</th><th>보고</th><th>상태</th>
            {scope === 'TEAM' && <th>담당</th>}
          </tr></thead>
          <tbody>{visible.map(item => <tr key={item.id} onClick={() => setDetail(item)}>
            <td>{item.category || '미분류'}</td>
            <td><strong>{item.title}</strong>
              {item.latestManagementAsk && <small className="cell-sub ask-line">요청 · {item.latestManagementAsk}</small>}</td>
            <td><div className="cell-progress"><i style={{ width: `${item.progress}%` }} /></div>
              <small>{item.progress}%{item.progressGain !== 0 && <span className="muted"> ({item.progressGain > 0 ? '+' : ''}{item.progressGain})</span>}</small></td>
            <td>{item.ageWeeks}주<small className="cell-sub">{item.firstWeek} ~</small></td>
            <td>{item.reportedWeeks}회{item.silentWeeks > 0 && <small className="cell-sub warn-text">{item.silentWeeks}주 누락</small>}</td>
            <td><div className="state-chips">
              {item.completed && <span className="state-chip done">완료</span>}
              {item.atRisk && <span className="state-chip risk">이슈 {item.issueWeeks}주</span>}
              {item.stalled && <span className="state-chip stalled">{item.stalledWeeks}주 정체</span>}
              {item.latestManagementAsk && <span className="state-chip ask">요청</span>}
              {item.repeatedPlan >= 3 && <span className="state-chip past">계획 {item.repeatedPlan}주 반복</span>}
              {!item.completed && !item.atRisk && !item.stalled && !item.latestManagementAsk && <span className="state-chip">진행</span>}
            </div></td>
            {scope === 'TEAM' && <td>{item.displayName}</td>}
          </tr>)}</tbody>
        </table></div>}
      </Card>
    </>}

    {detail && <div className="modal-backdrop" onClick={() => setDetail(undefined)}>
      <div className="modal wide" onClick={event => event.stopPropagation()}>
        <header>
          <div>
            <span className="week-label">{detail.category || '미분류'} · {detail.displayName}</span>
            <h2>{detail.title}</h2>
          </div>
          <button onClick={() => setDetail(undefined)}>×</button>
        </header>
        <div className="rollup-detail">
          <div className="rollup-detail-meta">
            <div><small>진척도</small><strong>{detail.startProgress}% → {detail.progress}%</strong></div>
            <div><small>경과</small><strong>{detail.ageWeeks}주</strong></div>
            <div><small>보고 주차</small><strong>{detail.reportedWeeks}회{detail.silentWeeks > 0 ? ` (${detail.silentWeeks} 누락)` : ''}</strong></div>
            <div><small>이슈 발생</small><strong>{detail.issueWeeks}주</strong></div>
          </div>
          {detail.latestManagementAsk && <div className="ask-panel">
            <strong>상위 조직 요청</strong><p>{detail.latestManagementAsk}</p>
          </div>}
          <h4 className="timeline-heading">주차별 기록</h4>
          <ol className="work-timeline">{detail.weeks.map(week => <li key={week.weekStart}>
            <div className="work-timeline-head">
              <strong>{week.weekStart}</strong>
              <span className="work-progress">{week.progress}%</span>
            </div>
            <div className="work-timeline-body">
              <div><b>실적</b><p>{week.currentResult || '-'}</p></div>
              <div><b>계획</b><p>{week.nextPlan || '-'}</p></div>
              {week.issue && <div><b>이슈</b><p className="warn-text">{week.issue}</p></div>}
              {week.managementAsk && <div><b>요청</b><p>{week.managementAsk}</p></div>}
            </div>
          </li>)}</ol>
        </div>
      </div>
    </div>}
  </>
}
