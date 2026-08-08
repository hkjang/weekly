import { useEffect, useState } from 'react'
import { api } from '../api'
import { Card, Empty, PageHeader, Spinner, StatusBadge, formatDate } from '../components'
import type { Report, ReportListItem } from '../types'

export default function ReportsPage({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [reports, setReports] = useState<ReportListItem[]>()
  const [selected, setSelected] = useState<Report>()
  const [status, setStatus] = useState('')
  const load = () => api<ReportListItem[]>(`/api/v1/reports${status ? `?status=${status}` : ''}`).then(setReports).catch(error => notify(error.message, 'error'))
  useEffect(() => { load() }, [status])
  const open = async (id: number) => { try { setSelected(await api<Report>(`/api/v1/reports/${id}`)) } catch (error) { notify(error instanceof Error ? error.message : '보고서를 열 수 없습니다.', 'error') } }
  return <><PageHeader title="과거 보고" description="주차와 상태별로 이전 주간보고를 조회하고 PPTX로 내보냅니다." action={<select value={status} onChange={e => setStatus(e.target.value)}><option value="">전체 상태</option><option value="DRAFT">작성 중</option><option value="SUBMITTED">검토 대기</option><option value="REVISION_REQUESTED">반려</option><option value="APPROVED">승인</option><option value="CLOSED">확정</option></select>} />
    {!reports ? <Spinner/> : !reports.length ? <Empty>조회할 보고서가 없습니다.</Empty> : <Card><div className="table-wrap"><table><thead><tr><th>주차</th><th>작성자</th><th>요약</th><th>상태</th><th>수정일</th><th/></tr></thead><tbody>{reports.map(report => <tr key={report.id} onClick={() => open(report.id)}><td>{report.weekStart}</td><td>{report.displayName}</td><td className="truncate">{report.summary || '-'}</td><td><StatusBadge status={report.status}/></td><td>{formatDate(report.updatedAt)}</td><td><a className="icon-button" href={`/api/v1/reports/${report.id}/export.pptx`} onClick={e => e.stopPropagation()} title="PPTX 다운로드">↓</a></td></tr>)}</tbody></table></div></Card>}
    {selected && <div className="modal-backdrop" onClick={() => setSelected(undefined)}><div className="modal wide" onClick={e => e.stopPropagation()}><header><div><StatusBadge status={selected.status}/><h2>{selected.displayName} · {selected.weekStart}</h2></div><button onClick={() => setSelected(undefined)}>×</button></header><p>{selected.summary}</p><div className="detail-items">{selected.items.map(item => <section key={item.id}><h3>{item.title} <small>{item.progress}%</small></h3><div><b>금주 실적</b><p>{item.currentResult || '-'}</p><b>차주 계획</b><p>{item.nextPlan || '-'}</p><b>이슈</b><p>{item.issue || '-'}</p></div></section>)}</div><footer><a className="button primary" href={`/api/v1/reports/${selected.id}/export.pptx`}>PPTX 다운로드</a></footer></div></div>}
  </>
}
