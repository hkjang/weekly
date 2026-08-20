import { useEffect, useState } from 'react'
import { errorText, api, post } from '../api'
import { Modal, Button, Card, Empty, PageHeader, SourceBadge, Spinner, StatusBadge } from '../components'
import ReportPresentation from '../ReportPresentation'
import type { Report, ReportListItem } from '../types'

export default function TeamPage({ workflowEnabled, currentUserId, notify }: { workflowEnabled: boolean; currentUserId: number; notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [reports, setReports] = useState<ReportListItem[]>()
  const [selected, setSelected] = useState<Report>()
  const [presenting, setPresenting] = useState(false)
  const load = () => api<ReportListItem[]>('/api/v1/team/reports').then(setReports)
  useEffect(() => { load() }, [])
  const open = (id: number) => api<Report>(`/api/v1/reports/${id}`).then(setSelected).catch(error => notify(error.message, 'error'))
  const review = async (action: 'approve' | 'reject') => { if (!selected) return; const comment = prompt(action === 'reject' ? '반려 사유를 입력하세요.' : '승인 의견(선택)') ?? ''; if (action === 'reject' && !comment.trim()) return; try { await post(`/api/v1/reports/${selected.id}/${action}`, { comment }); notify(action === 'approve' ? '승인했습니다.' : '반려했습니다.'); setSelected(undefined); await load() } catch (error) { notify(errorText(error, '처리할 수 없습니다.'), 'error') } }
  return <><PageHeader title="팀 주간보고" description={workflowEnabled ? '구성원의 보고서를 검토하고 승인 또는 반려합니다.' : '승인 절차 없이 확정된 구성원 보고서를 조회합니다.'}/>
    {!reports ? <Spinner/> : !reports.length ? <Empty>조회할 팀 보고서가 없습니다.</Empty> : <Card><div className="table-wrap"><table><thead><tr><th>주차</th><th>작성자</th><th>요약</th><th>상태</th><th>진행</th></tr></thead><tbody>{reports.map(report => <tr key={report.id} onClick={() => open(report.id)}><td>{report.weekStart}</td><td><strong>{report.displayName}</strong><small className="cell-sub">{report.username}</small></td><td className="truncate">{report.summary || '-'}</td><td><StatusBadge status={report.status}/></td><td><button className="text-button">열기 →</button></td></tr>)}</tbody></table></div></Card>}
    {selected && <Modal onClose={() => { setSelected(undefined); setPresenting(false) }} label={`${selected.displayName} ${selected.weekStart} 주간보고`} className="wide"><header><div><StatusBadge status={selected.status}/> <SourceBadge source={selected.sourceType}/><h2>{selected.displayName} · {selected.weekStart}</h2></div><button onClick={() => setSelected(undefined)}>×</button></header><p>{selected.summary}</p><div className="detail-items">{selected.items.map(item => <section key={item.id}><h3>{item.title} <small>{item.progress}%</small></h3><div><b>금주 실적</b><p>{item.currentResult || '-'}</p><b>차주 계획</b><p>{item.nextPlan || '-'}</p><b>이슈</b><p>{item.issue || '-'}</p></div></section>)}</div><footer><Button variant="secondary" onClick={() => setPresenting(true)}>▶ 발표 모드</Button><a className="button secondary" href={`/api/v1/reports/${selected.id}/export.pptx`}>PPTX 다운로드</a>{workflowEnabled && selected.status === 'SUBMITTED' && selected.userId !== currentUserId && <><Button variant="danger" onClick={() => review('reject')}>반려</Button><Button onClick={() => review('approve')}>승인</Button></>}</footer></Modal>}
    {presenting && selected && <ReportPresentation label={`${selected.displayName} · ${selected.weekStart}`}
      report={selected} onClose={() => setPresenting(false)} />}
  </>
}
