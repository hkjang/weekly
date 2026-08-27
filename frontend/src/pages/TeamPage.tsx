import { useEffect, useState } from 'react'
import { reviewDecision } from '../reviewAction'
import { errorText, api, post , download} from '../api'
import { Modal, Button, Card, Empty, PageHeader, SourceBadge, Spinner, StatusBadge } from '../components'
import ReportPresentation from '../ReportPresentation'
import ReportComments from '../ReportComments'
import type { Report, ReportListItem, ReportListView } from '../types'

export default function TeamPage({ workflowEnabled, currentUserId, notify }: { workflowEnabled: boolean; currentUserId: number; notify: (message: string, kind?: 'success' | 'error') => void }) {
  // Straight at the API through an <a> meant an expired session replaced the
  // application with a JSON error page. Fetched, the failure is a toast.
  const saveExport = async (reportId: number) => {
    try { await download(`/api/v1/reports/${reportId}/export.pptx`, `주간보고-${reportId}.pptx`) }
    catch (error) { notify(errorText(error, 'PPTX를 내려받을 수 없습니다.'), 'error') }
  }
  // The one thing a leader has to do on this screen is review what is waiting,
  // and there was no way to ask for it. On a 76 person organisation the list
  // arrives as a page of every status mixed together and the 승인/반려 buttons
  // only appear on SUBMITTED, so finding the work meant scrolling and reading
  // badges. The API has taken a status filter since the beginning; nothing
  // offered it.
  const [status, setStatus] = useState('')
  const [reports, setReports] = useState<ReportListItem[]>()
  const [total, setTotal] = useState(0)
  const [notice, setNotice] = useState('')
  const [selected, setSelected] = useState<Report>()
  const [presenting, setPresenting] = useState(false)
  const [busy, setBusy] = useState(false)
  // The list is a page, not the record. It used to be a bare LIMIT 500 with no
  // total beside it, so 3,120 reports arrived as 500 and the table looked like
  // everything there was.
  const query = (extra = '') => `/api/v1/team/reports?${status ? `status=${status}&` : ''}${extra}`
  const load = () => api<ReportListView>(query())
    .then(value => { setReports(value.items); setTotal(value.total); setNotice(value.notice ?? '') })
  const loadMore = async () => {
    if (!reports) return
    setBusy(true)
    try {
      const next = await api<ReportListView>(query(`offset=${reports.length}`))
      setReports([...reports, ...next.items])
      setTotal(next.total)
    } catch (error) {
      notify(errorText(error, '보고서를 더 불러올 수 없습니다.'), 'error')
    } finally { setBusy(false) }
  }
  // Re-reads from the first row when the filter changes: an offset carried over
  // from another filter points at a different list.
  useEffect(() => { setReports(undefined); load() }, [status])
  const open = (id: number) => api<Report>(`/api/v1/reports/${id}`).then(setSelected).catch(error => notify(error.message, 'error'))
  const review = async (action: 'approve' | 'reject') => { if (!selected) return; const decision = reviewDecision(action, prompt(action === 'reject' ? '반려 사유를 입력하세요.' : '승인 의견(선택)')); if (decision.kind === 'cancelled') return; if (decision.kind === 'needs-reason') { notify(decision.message, 'error'); return } const comment = decision.comment; try { await post(`/api/v1/reports/${selected.id}/${action}`, { comment }); notify(action === 'approve' ? '승인했습니다.' : '반려했습니다.'); setSelected(undefined); await load() } catch (error) { notify(errorText(error, '처리할 수 없습니다.'), 'error') } }
  return <><PageHeader title="팀 주간보고"
      description={workflowEnabled ? '구성원의 보고서를 검토하고 승인 또는 반려합니다.' : '승인 절차 없이 확정된 구성원 보고서를 조회합니다.'}
      action={<div className="header-actions">
        <label>상태<select value={status} onChange={event => setStatus(event.target.value)}>
          <option value="">전체</option>
          <option value="SUBMITTED">검토 대기</option>
          <option value="REVISION_REQUESTED">반려/수정</option>
          <option value="DRAFT">작성 중</option>
          <option value="APPROVED">승인</option>
          <option value="CLOSED">확정</option>
        </select></label>
      </div>} />
    {/* An empty list has more than one cause, and only one of them is the
        team's. When the server says which, say it instead of the blank. */}
    {!reports ? <Spinner/> : !reports.length ? <Empty>{notice || (status
      ? '이 상태인 보고서가 없습니다. 상태를 전체로 두면 나머지가 보입니다.'
      : '조회할 팀 보고서가 없습니다.')}</Empty> : <Card><div className="table-wrap"><table><thead><tr><th>주차</th><th>작성자</th><th>요약</th><th>상태</th><th>진행</th></tr></thead><tbody>{reports.map(report => <tr key={report.id} onClick={() => open(report.id)}><td>{report.weekStart}</td><td><strong>{report.displayName}</strong><small className="cell-sub">{report.username}</small></td><td className="truncate">{report.summary || '-'}</td><td><StatusBadge status={report.status}/></td><td><button className="text-button">열기 →</button></td></tr>)}</tbody></table></div>
      <div className="list-more">
        <span className="muted">{total.toLocaleString()}건 중 {reports.length.toLocaleString()}건</span>
        {reports.length < total && <Button variant="secondary" disabled={busy} onClick={loadMore}>
          {busy ? '불러오는 중…' : '더 보기'}</Button>}
      </div></Card>}
    {selected && <Modal onClose={() => { setSelected(undefined); setPresenting(false) }} label={`${selected.displayName} ${selected.weekStart} 주간보고`} className="wide"><header><div><StatusBadge status={selected.status}/> <SourceBadge source={selected.sourceType}/><h2>{selected.displayName} · {selected.weekStart}</h2></div><button onClick={() => setSelected(undefined)}>×</button></header><p>{selected.summary}</p><div className="detail-items">{selected.items.map(item => <section key={item.id}><h3>{item.title} <small>{item.progress}%</small></h3><div><b>금주 실적</b><p>{item.currentResult || '-'}</p><b>차주 계획</b><p>{item.nextPlan || '-'}</p><b>이슈</b><p>{item.issue || '-'}</p></div></section>)}</div><ReportComments reportId={selected.id} comments={selected.comments} notify={notify}
        onPosted={async () => { const fresh = await api<Report>(`/api/v1/reports/${selected.id}`); setSelected(fresh) }}
        placeholder="반려하지 않고 물어볼 수 있습니다. 보고서 상태는 바뀌지 않습니다." /><footer><Button variant="secondary" onClick={() => setPresenting(true)}>▶ 발표 모드</Button><Button variant="secondary" onClick={() => saveExport(selected.id)}>PPTX 다운로드</Button>{workflowEnabled && selected.status === 'SUBMITTED' && selected.userId !== currentUserId && <><Button variant="danger" onClick={() => review('reject')}>반려</Button><Button onClick={() => review('approve')}>승인</Button></>}</footer></Modal>}
    {presenting && selected && <ReportPresentation label={`${selected.displayName} · ${selected.weekStart}`}
      report={selected} onClose={() => setPresenting(false)} />}
  </>
}
