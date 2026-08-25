import { useEffect, useState } from 'react'
import { APIError, api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import type { AnalyticsOverview } from '../types'

interface EndpointMetric { method: string; route: string; requests: number; averageMs: number; maxMs: number; serverErrors: number; errorRate: number }

export default function AnalyticsPage({ isAdmin }: { isAdmin: boolean }) {
  const [overview, setOverview] = useState<AnalyticsOverview>()
  // Three different answers used to look the same. A team leader is not
  // allowed to read the API metrics, and turning that 403 into an empty list
  // told them the service had taken no calls in a day — which is not what
  // happened, and not something they could tell from the screen.
  const [endpoints, setEndpoints] = useState<EndpointMetric[] | 'forbidden' | 'failed'>()
  useEffect(() => { api<AnalyticsOverview>('/api/v1/analytics/overview').then(setOverview); if (isAdmin) api<EndpointMetric[]>('/api/v1/analytics/endpoints').then(setEndpoints).catch(error => setEndpoints(error instanceof APIError && error.status === 403 ? 'forbidden' : 'failed')); else setEndpoints('forbidden') }, [])
  if (!overview) return <Spinner/>
  const statuses = [{ key: 'DRAFT', name: '작성 중', color: '#94a3b8' }, { key: 'SUBMITTED', name: '검토 대기', color: '#f59e0b' }, { key: 'REVISION_REQUESTED', name: '반려', color: '#ef4444' }, { key: 'APPROVED', name: '승인', color: '#16a34a' }, { key: 'CLOSED', name: '확정', color: '#2563eb' }]
  const maximum = Math.max(1, ...statuses.map(s => overview.statusCounts[s.key] ?? 0))
  return <><PageHeader title="보고 · 서비스 분석" description={endpoints === 'forbidden' ? '조직 제출 현황을 분석합니다.' : '조직 제출 현황과 Weekly API 운영 상태를 함께 분석합니다.'}/>
    <div className="metric-grid"><Card><span className="metric-label">제출률</span><strong className="metric-value">{overview.submissionRate.toFixed(1)}%</strong><small>{overview.submittedUsers} / {overview.totalUsers}명</small></Card><Card><span className="metric-label">등록 이슈</span><strong className="metric-value">{overview.openIssues}</strong><small>이번 주 지원 요청</small></Card><Card><span className="metric-label">평균 진척도</span><strong className="metric-value">{overview.averageProgress.toFixed(1)}%</strong><small>업무 항목 기준</small></Card><Card><span className="metric-label">기준 주차</span><strong className="metric-date">{overview.weekStart}</strong><small>주차 시작일</small></Card></div>
    <Card title="보고서 상태 분포"><div className="bar-chart">{statuses.map(status => <div className="bar-row" key={status.key}><span>{status.name}</span><div><i style={{ width: `${((overview.statusCounts[status.key] ?? 0) / maximum) * 100}%`, background: status.color }}/></div><strong>{overview.statusCounts[status.key] ?? 0}</strong></div>)}</div></Card>
    {endpoints !== 'forbidden' && <Card title="최근 24시간 API 분석" action={<span className="mcp-badge">MCP 제공</span>}>{endpoints === undefined ? <Spinner/> : endpoints === 'failed' ? <Empty>API 분석을 불러오지 못했습니다. 잠시 후 다시 열어 보십시오.</Empty> : endpoints.length === 0 ? <Empty>최근 24시간 동안 기록된 API 호출이 없습니다.</Empty> : <div className="table-wrap"><table><thead><tr><th>메서드</th><th>경로</th><th>호출</th><th>평균</th><th>최대</th><th>오류율</th></tr></thead><tbody>{endpoints.map((item, index) => <tr key={index}><td><code>{item.method}</code></td><td><code>{item.route}</code></td><td>{item.requests.toLocaleString()}</td><td>{item.averageMs} ms</td><td>{item.maxMs} ms</td><td className={item.errorRate > 5 ? 'danger-text' : ''}>{item.errorRate}%</td></tr>)}</tbody></table></div>}</Card>}
  </>
}
