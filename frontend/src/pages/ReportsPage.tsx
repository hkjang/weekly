import { useEffect, useState } from 'react'
import { errorText, api, del, post, put , download} from '../api'
import { Modal, Button, Card, Empty, PageHeader, SourceBadge, Spinner, StatusBadge, formatDate , openable} from '../components'
import AttachmentPanel from '../AttachmentPanel'
import EvidenceUses from '../EvidenceUses'
import ReportPresentation from '../ReportPresentation'
import IncludedReportMaterials from '../IncludedReportMaterials'
import AutoResizeTextarea from '../AutoResizeTextarea'
import { registerUnsavedGuard } from '../unsavedGuard'
import type { ItemSourceKind, Report, ReportItem, ReportListItem, ReportListView } from '../types'

// What a source chip is called. The kinds the model allows but no connector
// writes yet are here too, so adding one is a server change and not a screen
// change as well.
const sourceKindName: Record<ItemSourceKind, string> = {
  MANUAL: '직접 작성', CONFLUENCE: 'Confluence', PPTX: 'PPTX', AI_TEXT: 'AI 초안',
  JIRA: 'Jira', GIT: 'Git', CI: 'CI', ITSM: 'ITSM', API: 'API',
}

const blankItem = (): ReportItem => ({ category: '', title: '', currentResult: '', nextPlan: '', issue: '', managementAsk: '', progress: 0, sortOrder: 0 })

export default function ReportsPage({ currentWeekStart, openReportId, notify }: {
  currentWeekStart: string
  openReportId?: number
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  // Straight at the API through an <a> meant an expired session replaced the
  // application with a JSON error page. Fetched, the failure is a toast.
  const saveExport = async (reportId: number) => {
    try { await download(`/api/v1/reports/${reportId}/export.pptx`, `주간보고-${reportId}.pptx`) }
    catch (error) { notify(errorText(error, 'PPTX를 내려받을 수 없습니다.'), 'error') }
  }
  const [reports, setReports] = useState<ReportListItem[]>()
  const [total, setTotal] = useState(0)
  const [loadingMore, setLoadingMore] = useState(false)
  // Which source the reader is tracing back. One at a time: this is a detour
  // from reading the report, not a second thing to read alongside it.
  const [tracing, setTracing] = useState<{ kind: string; reference: string; label: string }>()
  const [selected, setSelected] = useState<Report>()
  const [status, setStatus] = useState('')
  const [editing, setEditing] = useState(false)
  const [summary, setSummary] = useState('')
  const [items, setItems] = useState<ReportItem[]>([])
  const [busy, setBusy] = useState(false)
  const [cloning, setCloning] = useState(false)
  const [presenting, setPresenting] = useState(false)
  const [cloneWeek, setCloneWeek] = useState(currentWeekStart)
  const [cloneMode, setCloneMode] = useState<'STRUCTURE' | 'FULL'>('STRUCTURE')
  const query = (extra = '') => `/api/v1/reports?${status ? `status=${status}&` : ''}${extra}`
  // A load that fails has to stop looking like a load that is still running.
  // Measured by failing this screen's own request in a browser: the spinner
  // stayed up for as long as the page was open, and the only word about it was
  // a toast that fades. A reader waits for something that will never arrive.
  const [failed, setFailed] = useState('')
  const load = () => api<ReportListView>(query())
    .then(value => { setFailed(''); setReports(value.items); setTotal(value.total) })
    // errorText, not error.message: a request that never reached the server
    // arrives as the browser's own English string.
    .catch(error => {
      setFailed(errorText(error, '보고서 목록을 불러오지 못했습니다.'))
      notify(errorText(error, '보고서를 열 수 없습니다.'), 'error')
    })
  const loadMore = async () => {
    if (!reports) return
    setLoadingMore(true)
    try {
      const next = await api<ReportListView>(query(`offset=${reports.length}`))
      setReports([...reports, ...next.items]); setTotal(next.total)
    } catch (error) {
      notify(errorText(error, '보고서를 더 불러올 수 없습니다.'), 'error')
    } finally { setLoadingMore(false) }
  }
  useEffect(() => { void load() }, [status])
  // Quick navigation can name a report; open it once the list has loaded.
  useEffect(() => { if (openReportId) void open(openReportId) }, [openReportId])
  const open = async (id: number) => { try { const report = await api<Report>(`/api/v1/reports/${id}`); setSelected(report); setSummary(report.summary); setItems(report.items.map(item => ({ ...item }))); setEditing(false); setCloning(false) } catch (error) { notify(errorText(error, '보고서를 열 수 없습니다.'), 'error') } }
  // Escape now closes this dialog, and it never used to. Discarding a rewritten
  // report to a reflex keypress — Escape also dismisses an input method's
  // candidate list — would be a poor trade for the convenience.
  const hasPendingEdits = () => {
    if (!editing || !selected) return false
    return summary !== selected.summary
      || JSON.stringify(items.map(item => [item.category, item.title, item.currentResult, item.nextPlan, item.issue, item.managementAsk ?? '', item.progress]))
      !== JSON.stringify(selected.items.map(item => [item.category, item.title, item.currentResult, item.nextPlan, item.issue, item.managementAsk ?? '', item.progress]))
  }
  const confirmDiscardEdits = () => !hasPendingEdits() || confirm('수정한 내용을 저장하지 않고 닫으시겠습니까?')
  // Closing the dialog asks, but closing the dialog was never the only way out.
  // The backdrop blocks the sidebar; the browser's own back button and reload go
  // straight past it, and both used to discard a rewritten report without a
  // word. Telling the app-wide guard about these edits covers those exits too.
  useEffect(() => {
    registerUnsavedGuard(hasPendingEdits)
    return () => registerUnsavedGuard(null)
  }, [editing, selected, summary, items])
  const close = () => { setPresenting(false); setSelected(undefined); setEditing(false); setCloning(false) }
  const updateItem = (index: number, values: Partial<ReportItem>) => setItems(current => current.map((item, itemIndex) => itemIndex === index ? { ...item, ...values } : item))
  const save = async () => { if (!selected) return; const prepared = items.filter(item => item.title.trim()); if (!prepared.length) { notify('업무 항목을 하나 이상 입력하세요.', 'error'); return } setBusy(true); try { await put(`/api/v1/reports/${selected.id}`, { version: selected.version, summary, sourceType: selected.sourceType, items: prepared.map((item, index) => ({ id: item.id, category: item.category, title: item.title, currentResult: item.currentResult, nextPlan: item.nextPlan, issue: item.issue, managementAsk: item.managementAsk ?? '', progress: item.progress, sortOrder: index })) }); await Promise.all([load(), open(selected.id)]); setEditing(false); notify('주간보고 수정사항을 저장했습니다.') } catch (error) { notify(errorText(error, '보고서를 수정할 수 없습니다.'), 'error') } finally { setBusy(false) } }
  const remove = async () => { if (!selected || !confirm(`${selected.weekStart} 주간보고를 삭제하시겠습니까? 삭제한 보고서는 복구할 수 없습니다.`)) return; setBusy(true); try { await del(`/api/v1/reports/${selected.id}?version=${selected.version}`); close(); await load(); notify('주간보고를 삭제했습니다.') } catch (error) { notify(errorText(error, '보고서를 삭제할 수 없습니다.'), 'error') } finally { setBusy(false) } }
  const startClone = () => { setCloneWeek(currentWeekStart); setCloneMode('STRUCTURE'); setCloning(true) }
  const duplicateReport = async () => { if (!selected || !cloneWeek) { notify('복제할 대상 주차를 선택하세요.', 'error'); return } setBusy(true); try { const created = await post<{ id: number; weekStart: string }>(`/api/v1/reports/${selected.id}/clone`, { targetWeekStart: cloneWeek, mode: cloneMode }); await load(); await open(created.id); notify(`${created.weekStart} 주차의 새 초안을 만들었습니다.`) } catch (error) { notify(errorText(error, '보고서를 복제할 수 없습니다.'), 'error') } finally { setBusy(false) } }

  return <><PageHeader title="과거 보고" description="이전 주간보고를 조회하고 복제·수정·삭제하거나 PPTX로 내보냅니다." action={<select aria-label="조회 범위" value={status} onChange={e => setStatus(e.target.value)}><option value="">전체 상태</option><option value="DRAFT">작성 중</option><option value="SUBMITTED">검토 대기</option><option value="REVISION_REQUESTED">반려</option><option value="APPROVED">승인</option><option value="CLOSED">확정</option></select>} />
    {failed ? <Card><Empty>{failed}</Empty><div className="audit-pager">
      <Button variant="secondary" onClick={() => { setFailed(''); void load() }}>다시 시도</Button></div></Card> : !reports ? <Spinner/> : !reports.length ? <Empty action={<Button onClick={() => { window.location.hash = '#/current' }}>이번 주 보고서 작성</Button>}>아직 저장한 보고서가 없습니다. 이번 주 보고서를 작성하면 여기에 쌓입니다.</Empty> : <Card><div className="table-wrap"><table><thead><tr><th>주차</th><th>작성자</th><th>요약</th><th>출처</th><th>상태</th><th>수정일</th><th/></tr></thead><tbody>{reports.map(report => <tr key={report.id} {...openable(() => open(report.id), `${report.weekStart} 주간보고 열기`)}><td>{report.weekStart}</td><td>{report.displayName}</td><td className="truncate">{report.summary || '-'}</td><td><SourceBadge source={report.sourceType}/></td><td><StatusBadge status={report.status}/></td><td>{formatDate(report.updatedAt)}</td><td><button className="icon-button" onClick={e => { e.stopPropagation(); void saveExport(report.id) }} title="PPTX 다운로드">↓</button></td></tr>)}</tbody></table></div>
      <div className="list-more">
        <span className="muted">{total.toLocaleString()}건 중 {reports.length.toLocaleString()}건</span>
        {reports.length < total && <Button variant="secondary" disabled={loadingMore} onClick={loadMore}>
          {loadingMore ? '불러오는 중…' : '더 보기'}</Button>}
      </div></Card>}
    {selected && <Modal onClose={close} beforeClose={confirmDiscardEdits} label={`${selected.displayName} ${selected.weekStart} 주간보고`} className="wide"><header><div><StatusBadge status={selected.status}/> <SourceBadge source={selected.sourceType}/><h2>{selected.displayName} · {selected.weekStart}</h2></div><button onClick={() => { if (confirmDiscardEdits()) close() }}>×</button></header>{editing ? <div className="history-editor"><label>주간 요약<AutoResizeTextarea value={summary} onChange={e => setSummary(e.target.value)} maxLength={10000}/></label>{(selected.status === 'SUBMITTED' || selected.status === 'APPROVED' || selected.status === 'CLOSED') && <div className="edit-notice">{selected.status === 'CLOSED' ? '저장하면 작성 중 상태로 돌아갑니다. 다시 제출해야 확정됩니다.' : '저장하면 기존 검토 결과가 해제되고 작성 중 상태로 돌아갑니다.'}</div>}{items.map((item, index) => <section key={item.id ?? index}><header><strong>업무 {index + 1}</strong>{items.length > 1 && <button className="remove-button" onClick={() => setItems(current => current.filter((_, itemIndex) => itemIndex !== index))}>항목 삭제</button>}</header><div className="form-grid title-grid"><label>구분<input value={item.category} maxLength={80} onChange={e => updateItem(index, { category: e.target.value })}/></label><label>업무 제목<input value={item.title} maxLength={240} onChange={e => updateItem(index, { title: e.target.value })}/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} onChange={e => updateItem(index, { progress: Number(e.target.value) })}/></label></div><div className="form-grid content-grid"><label>금주 실적<AutoResizeTextarea value={item.currentResult} maxLength={20000} onChange={e => updateItem(index, { currentResult: e.target.value })}/></label><label>차주 계획<AutoResizeTextarea value={item.nextPlan} maxLength={20000} onChange={e => updateItem(index, { nextPlan: e.target.value })}/></label><label>이슈<AutoResizeTextarea value={item.issue} maxLength={20000} onChange={e => updateItem(index, { issue: e.target.value })}/></label><label>상위 조직 요청<AutoResizeTextarea value={item.managementAsk ?? ''} maxLength={5000} onChange={e => updateItem(index, { managementAsk: e.target.value })}/></label></div></section>)}<Button variant="secondary" onClick={() => setItems(current => [...current, blankItem()])}>+ 업무 항목 추가</Button></div> : <><p>{selected.summary}</p><div className="detail-items">{selected.items.map(item => <section key={item.id}><h3>{item.title} <small>{item.progress}%</small></h3><div><b>금주 실적</b><p>{item.currentResult || '-'}</p><b>차주 계획</b><p>{item.nextPlan || '-'}</p><b>이슈</b><p>{item.issue || '-'}</p>{item.managementAsk && <><b>상위 조직 요청</b><p>{item.managementAsk}</p></>}</div>{item.sources && item.sources.length > 0 && <div className="item-sources"><b>근거 {item.sources.length}건</b>{item.sources.map((source, index) => <button key={index} className={`source-chip ${source.kind.toLowerCase()}`} title="이 근거를 쓴 다른 보고 보기" onClick={() => setTracing(tracing && tracing.reference === (source.reference ?? '') ? undefined : { kind: source.kind, reference: source.reference ?? '', label: source.title || source.kind })}>{sourceKindName[source.kind] ?? source.kind}{source.title ? ` · ${source.title}` : ''}{source.detail ? ` · ${source.detail}` : ''}</button>)}</div>}{tracing && item.sources?.some(source => (source.reference ?? '') === tracing.reference) && <EvidenceUses kind={tracing.kind} reference={tracing.reference} label={tracing.label} notify={notify} onClose={() => setTracing(undefined)} />}</section>)}</div><IncludedReportMaterials materials={selected.includedMaterials ?? []}/><div className="detail-attachments"><AttachmentPanel reportId={selected.id} editable notify={notify}/></div>{cloning && <div className="clone-panel"><div><strong>새 주차로 복제</strong><p>새 보고서는 작성 중 상태로 생성되며 승인 이력·댓글·외부 출처 연결은 복사하지 않습니다.</p></div><label>대상 주차 시작일<input type="date" value={cloneWeek} onChange={e => setCloneWeek(e.target.value)}/></label><label>복제 범위<select value={cloneMode} onChange={e => setCloneMode(e.target.value as 'STRUCTURE' | 'FULL')}><option value="STRUCTURE">업무 구조만 복제 (권장)</option><option value="FULL">요약·실적·계획·이슈 전체 복제</option></select></label><div className="clone-actions"><Button variant="secondary" onClick={() => setCloning(false)} disabled={busy}>취소</Button><Button onClick={duplicateReport} disabled={busy}>{busy ? '복제 중…' : '새 초안 만들기'}</Button></div></div>}</>}<footer>{editing ? <><Button variant="secondary" onClick={() => { setEditing(false); setSummary(selected.summary); setItems(selected.items.map(item => ({ ...item }))) }} disabled={busy}>취소</Button><Button onClick={save} disabled={busy}>{busy ? '저장 중…' : '수정 저장'}</Button></> : <><Button variant="danger" onClick={remove} disabled={busy}>삭제</Button><Button variant="secondary" onClick={() => setEditing(true)} disabled={cloning}>수정</Button><Button variant="secondary" onClick={startClone} disabled={busy || cloning}>복제</Button><Button variant="secondary" onClick={() => setPresenting(true)}>▶ 발표 모드</Button><Button onClick={() => saveExport(selected.id)}>PPTX 다운로드</Button></>}</footer></Modal>}
    {presenting && selected && <ReportPresentation label={`${selected.displayName} · ${selected.weekStart}`}
      report={selected} onClose={() => setPresenting(false)} />}
  </>
}
