import { useEffect, useRef, useState } from 'react'
import IssueDependency from '../IssueDependency'
import ReportComments from '../ReportComments'
import { revisionReasonOf } from '../reportReview'
import { errorText, api, del, patch, post, put , download} from '../api'
import { Button, Card, Empty, PageHeader, SourceBadge, Spinner, StatusBadge } from '../components'
import AttachmentPanel from '../AttachmentPanel'
import ReportPresentation from '../ReportPresentation'
import IncludedReportMaterials from '../IncludedReportMaterials'
import AutoResizeTextarea from '../AutoResizeTextarea'
import { registerUnsavedGuard } from '../unsavedGuard'
import type { AIWeeklyResult, ConfluenceCandidate, ConfluenceCandidateResponse, IncludedReportMaterial, IncludedReportMaterialsView, OpenFollowUp, PreviousPlan, PreviousPlanItem, QualityReport, Report, ReportItem } from '../types'

const blankItem = (): ReportItem => ({ category: '', title: '', currentResult: '', nextPlan: '', issue: '', managementAsk: '', progress: 0, sortOrder: 0 })

export default function ReportEditorPage({ workflowEnabled, aiEnabled, confluenceEnabled, notify }: { workflowEnabled: boolean; aiEnabled: boolean; confluenceEnabled: boolean; notify: (message: string, kind?: 'success' | 'error') => void }) {
  // Straight at the API through an <a> meant an expired session replaced the
  // application with a JSON error page. Fetched, the failure is a toast.
  const saveExport = async (reportId: number) => {
    try { await download(`/api/v1/reports/${reportId}/export.pptx`, `주간보고-${reportId}.pptx`) }
    catch (error) { notify(errorText(error, 'PPTX를 내려받을 수 없습니다.'), 'error') }
  }
  const [report, setReport] = useState<Report | null>()
  const [summary, setSummary] = useState('')
  const [items, setItems] = useState<ReportItem[]>([blankItem()])
  const [busy, setBusy] = useState(false)
  const [aiText, setAIText] = useState('')
  const [aiResult, setAIResult] = useState<AIWeeklyResult>()
  const [aiBusy, setAIBusy] = useState(false)
  const [aiApplied, setAIApplied] = useState(false)
  const [candidateData, setCandidateData] = useState<ConfluenceCandidateResponse>()
  const [previous, setPrevious] = useState<PreviousPlan | null>(null)
  // What the author agreed to and has not written down yet. Without this the
  // record stops at the meeting: next Monday they open a blank editor with no
  // sign that a decision put anything on their plate.
  const [followUps, setFollowUps] = useState<OpenFollowUp[]>([])
  const [includedMaterials, setIncludedMaterials] = useState<IncludedReportMaterial[]>()
  const [includedMaterialsFailed, setIncludedMaterialsFailed] = useState(false)
  const [quality, setQuality] = useState<QualityReport>()
  const [pendingCandidateIDs, setPendingCandidateIDs] = useState<number[]>([])
  const [captureCount, setCaptureCount] = useState(0)
  const [presenting, setPresenting] = useState(false)
  // The baseline is what the server last confirmed. Anything that differs from
  // it is work that only exists in this tab.
  // Last week's plan and the open follow-ups are how work crosses a week
  // boundary. Both are drawn only when they have something, so a failed request
  // does not say anything wrong — it simply removes the carry-over, and the
  // writer starts the week believing they had nothing to carry.
  //
  // One flag for both was wrong and the browser said so: whichever request
  // finished second decided the answer, and a successful previous-week fetch
  // erased the follow-up failure that had just been recorded.
  const [previousFailed, setPreviousFailed] = useState(false)
  const [followUpsFailed, setFollowUpsFailed] = useState(false)
  // Without this the screen spun for ever. `report` stays undefined until the
  // request answers, and the render below shows a spinner while it is — so a
  // failed request left a writer looking at a loading indicator with no editor,
  // no message and nothing to retry. Measured: two page errors and a spinner
  // that never resolved.
  const [loadFailed, setLoadFailed] = useState(false)
  const savedSnapshot = useRef('')
  // Confluence is optional, and a deployment without it answers this call with
  // a configuration error every single time. Unhandled, that rejected the
  // Promise.all below and threw on every visit to the screen people use most.
  // The section only draws when it has candidates, so failing quietly here
  // shows nothing — and an operator who has configured Confluence sees its
  // health on the admin screen, which is where that belongs.
  const loadCandidates = () => confluenceEnabled
    ? api<ConfluenceCandidateResponse>('/api/v1/reports/current/candidates').then(setCandidateData).catch(() => setCandidateData(undefined))
    : Promise.resolve()
  const loadPrevious = () => api<PreviousPlan | null>('/api/v1/reports/previous').then(value => { setPrevious(value); setPreviousFailed(false) }).catch(() => { setPrevious(null); setPreviousFailed(true) })
  const loadFollowUps = () => api<OpenFollowUp[]>('/api/v1/decisions/open').then(value => { setFollowUps(value); setFollowUpsFailed(false) }).catch(() => { setFollowUps([]); setFollowUpsFailed(true) })
  const loadIncludedMaterials = () => api<IncludedReportMaterialsView>('/api/v1/reports/current/included-materials')
    .then(value => { setIncludedMaterials(value.materials); setIncludedMaterialsFailed(false) })
    .catch(() => { setIncludedMaterialsFailed(true) })
  const load = async () => { try { const value = await api<Report | null>('/api/v1/reports/current'); setReport(value); if (value) { setSummary(value.summary); setItems(value.items.length ? value.items : [blankItem()]); setAIApplied(value.sourceType === 'AI_TEXT') } savedSnapshot.current = snapshotOf(value?.summary ?? '', value?.items ?? []); setLoadFailed(false) } catch { setLoadFailed(true) } }
  // Used when a save fails: the version and status have to catch up, but the
  // content on screen must not be replaced by the server's copy. Overwriting it
  // here would destroy exactly the work the failed save was trying to keep.
  const refreshMeta = async () => { const value = await api<Report | null>('/api/v1/reports/current'); setReport(value) }
  useEffect(() => { void Promise.all([load(), loadCandidates(), loadPrevious(), loadFollowUps(), loadIncludedMaterials()]) }, [])
  // The screen people open every week put the caret nowhere, so writing began
  // with a hunt for a field. Scrolling is suppressed: moving the page under
  // someone who came back to a finished report would be worse than no focus.
  const focused = useRef(false)
  useEffect(() => {
    if (focused.current || report === undefined) return
    focused.current = true
    const target = summary.trim() === ''
      ? document.querySelector<HTMLElement>('.summary-input')
      : document.querySelector<HTMLElement>('.report-item input[placeholder="업무명을 입력하세요"]')
    if (target && (target as HTMLInputElement).value === '') target.focus({ preventScroll: true })
  }, [report, summary])
  // Derived on every render so the screen can say whether the work in front of
  // the author is safe. Until now the only sign was the confirmation dialog on
  // the way out, which tells you too late and only if you try to leave.
  const dirty = snapshotOf(summary, items) !== savedSnapshot.current

  // Registered once, reading the latest state through a ref: the guard is asked
  // at navigation time, not at render time, so it must not close over a stale
  // copy and must not be torn down and rebuilt on every keystroke.
  const liveRef = useRef({ summary, items })
  liveRef.current = { summary, items }
  // Checked against the author's own history while they write, not after they
  // submit. The debounce is the whole cost control: one request per pause in
  // typing rather than one per keystroke.
  useEffect(() => {
    const filled = items.filter(item => item.title.trim())
    if (!filled.length) { setQuality(undefined); return }
    const timer = window.setTimeout(() => {
      post<QualityReport>('/api/v1/reports/quality', { weekStart: report?.weekStart, items: filled })
        .then(setQuality).catch(() => setQuality(undefined))
    }, 1200)
    return () => window.clearTimeout(timer)
  }, [items, report?.weekStart])
  useEffect(() => {
    registerUnsavedGuard(() => snapshotOf(liveRef.current.summary, liveRef.current.items) !== savedSnapshot.current)
    return () => registerUnsavedGuard(null)
  }, [])
  if (loadFailed) return <Empty>이번 주 보고서를 불러오지 못했습니다. 작성한 내용이 사라진 것은 아니며, 화면을 다시 열어 보십시오.</Empty>
  if (report === undefined) return <Spinner />
  const editable = true
  // The dedicated endpoint is needed before the owner has created a report.
  // Once a report exists its response carries the same material, so keep that
  // useful fallback if only the supplementary request failed.
  const effectiveMaterials = includedMaterials ?? report?.includedMaterials ?? []
  const changeItem = (index: number, patch: Partial<ReportItem>) => setItems(items.map((item, i) => i === index ? { ...item, ...patch } : item))
  const validItems = () => items.filter(item => item.title.trim())
  // Pairing is by work item first and by normalized title second: a saved item
  // carries the identifier the server resolved, an item still being typed does
  // not. The title rule is the server's planMatchKey, kept identical on purpose.
  const planKey = (title: string) => title.toLowerCase().replace(/\s+/g, '')
  const previousItems = previous?.items ?? []
  const previousFor = (item: ReportItem) => previousItems.find(plan => (!!item.workItemId && plan.workItemId === item.workItemId) || (!!item.title.trim() && plan.matchKey === planKey(item.title)))
  const reportedKeys = new Set(validItems().map(item => planKey(item.title)))
  // Only what is not already in this week's report. A follow-up the author has
  // written up is finished business as far as this card is concerned.
  const openFollowUps = followUps.filter(entry => !reportedKeys.has(planKey(entry.workTitle)))
  const overdueFollowUps = openFollowUps.filter(entry => entry.overdue).length
  const unreported = previousItems.filter(plan => plan.carryOver && !reportedKeys.has(plan.matchKey))
  // Carrying a follow-up forward writes it into 차주 계획, not 금주 실적: it is
  // what was agreed to do, not a claim that it has been done.
  const carryFollowUp = (entry: OpenFollowUp) => {
    const kept = validItems()
    setItems([...kept, { category: entry.category, title: entry.workTitle, currentResult: '',
      nextPlan: entry.followUp, issue: '', managementAsk: '', progress: 0, sortOrder: kept.length }])
  }
  const carryForward = (plan: PreviousPlanItem) => { const kept = validItems(); setItems([...kept, { category: plan.category, title: plan.title, currentResult: '', nextPlan: '', issue: '', managementAsk: '', progress: plan.progress, sortOrder: kept.length }]) }
  const save = async () => { const preparedItems = validItems(); if (!preparedItems.length) { notify('업무 항목을 하나 이상 입력하세요.', 'error'); return false } const acceptedCandidateIDs = Array.from(new Set(preparedItems.flatMap(item => item.candidateId ? [item.candidateId] : []))); setBusy(true); try { const payload = { summary, sourceType: acceptedCandidateIDs.length ? 'CONFLUENCE_AI' : aiApplied ? 'AI_TEXT' : 'MANUAL', items: preparedItems.map(item => ({ id: item.id, category: item.category, title: item.title, currentResult: item.currentResult, nextPlan: item.nextPlan, issue: item.issue, managementAsk: item.managementAsk ?? '', progress: item.progress, sortOrder: item.sortOrder, issueOutcome: item.issueOutcome })) }; const saved = report ? await put<{ id: number }>(`/api/v1/reports/${report.id}`, { ...payload, version: report.version }) : await post<{ id: number }>('/api/v1/reports', payload); if (acceptedCandidateIDs.length) await post('/api/v1/report-candidates/accept', { ids: acceptedCandidateIDs, reportId: saved.id }); setPendingCandidateIDs([]); await Promise.all([load(), loadCandidates(), loadIncludedMaterials()]); notify('보고서를 저장했습니다.'); return true } catch (error) { await refreshMeta().catch(() => undefined); notify(errorText(error, '저장할 수 없습니다. 작성 중인 내용은 그대로 남아 있습니다.'), 'error'); return false } finally { setBusy(false) } }
  const analyzeAI = async () => { if (!aiText.trim()) { notify('AI가 분석할 주간업무 내용을 입력하세요.', 'error'); return } setAIBusy(true); try { const result = await post<AIWeeklyResult>('/api/v1/ai/reports/parse-text', { text: aiText }); setAIResult(result); notify(`${result.reportItems.length}개 업무 항목을 구조화했습니다.`) } catch (error) { notify(errorText(error, 'AI 분석을 완료할 수 없습니다.'), 'error') } finally { setAIBusy(false) } }
  const changeAIItem = (index: number, patch: Partial<AIWeeklyResult['reportItems'][number]>) => setAIResult(aiResult ? { ...aiResult, reportItems: aiResult.reportItems.map((item, i) => i === index ? { ...item, ...patch } : item) } : undefined)
  const applyAI = (mode: 'merge' | 'replace') => { if (!aiResult) return; const incoming = aiResult.reportItems.filter(item => item.title.trim()).map((item, index) => ({ category: item.category, title: item.title, currentResult: item.currentResult, nextPlan: item.nextPlan, issue: item.issue, managementAsk: '', progress: item.progress, sortOrder: index })); if (!incoming.length) { notify('적용할 AI 업무 항목이 없습니다.', 'error'); return } setItems(mode === 'replace' ? incoming : mergeReportItems(validItems(), incoming)); if (aiResult.summary && (mode === 'replace' || !summary.trim())) setSummary(aiResult.summary); setAIApplied(true); setAIResult(undefined); notify(mode === 'merge' ? 'AI 결과를 기존 보고서와 병합했습니다. 저장 전 내용을 확인하세요.' : 'AI 결과로 업무 항목을 교체했습니다. 저장 전 내용을 확인하세요.') }
  const changeCandidate = (id: number, values: Partial<ConfluenceCandidate>) => setCandidateData(candidateData ? { ...candidateData, candidates: candidateData.candidates.map(item => item.id === id ? { ...item, ...values } : item) } : undefined)
  const saveCandidate = async (candidate: ConfluenceCandidate) => { try { await patch(`/api/v1/report-candidates/${candidate.id}`, { normalizedTitle: candidate.normalizedTitle, category: candidate.category, currentResult: candidate.currentResult, nextPlan: candidate.nextPlan, issue: candidate.issue }); await loadCandidates(); notify('Confluence 자동 초안을 수정했습니다.') } catch (error) { notify(errorText(error, '자동 초안을 수정할 수 없습니다.'), 'error') } }
  const ignoreCandidate = async (candidate: ConfluenceCandidate) => { if (pendingCandidateIDs.includes(candidate.id)) { notify('이미 보고서에 반영한 초안은 아래 업무 항목에서 편집하거나 삭제하세요.', 'error'); return } if (!confirm(`'${candidate.normalizedTitle}' 자동 초안을 이번 주에서 제외하시겠습니까?`)) return; try { await del(`/api/v1/report-candidates/${candidate.id}`); await loadCandidates(); notify('제외한 문서는 다음 동기화에서도 다시 생성되지 않습니다.') } catch (error) { notify(errorText(error, '자동 초안을 제외할 수 없습니다.'), 'error') } }
  const applyCandidate = (candidate: ConfluenceCandidate) => { const incoming: ReportItem = { candidateId: candidate.id, category: candidate.category, title: candidate.normalizedTitle, currentResult: candidate.currentResult, nextPlan: candidate.nextPlan, issue: candidate.issue, managementAsk: '', progress: 0, sortOrder: validItems().length }; setItems(mergeReportItems(validItems(), [incoming])); setPendingCandidateIDs(Array.from(new Set([...pendingCandidateIDs, candidate.id]))); notify('자동 초안을 업무 항목에 반영했습니다. 저장 전 내용을 확인하세요.') }
  const submit = async () => { let current = report; if (!current) { const saved = await save(); if (!saved) return; current = await api<Report | null>('/api/v1/reports/current') } else { const saved = await save(); if (!saved) return; current = await api<Report | null>('/api/v1/reports/current') } if (!current || !confirm(workflowEnabled ? '팀장 검토를 위해 제출하시겠습니까?' : '제출하면 바로 확정됩니다. 계속하시겠습니까?')) return; setBusy(true); try { await post(`/api/v1/reports/${current.id}/submit`); await load(); notify(workflowEnabled ? '보고서를 제출했습니다.' : '보고서를 확정했습니다.') } catch (error) { notify(errorText(error, '제출할 수 없습니다.'), 'error') } finally { setBusy(false) } }
  const removeReport = async () => { if (!report || !confirm(`${report.weekStart} 주간보고를 삭제하시겠습니까? 삭제한 보고서는 복구할 수 없습니다.`)) return; setBusy(true); try { await del(`/api/v1/reports/${report.id}?version=${report.version}`); setReport(null); setSummary(''); setItems([blankItem()]); setAIApplied(false); setPendingCandidateIDs([]); savedSnapshot.current = snapshotOf('', []); await loadCandidates(); notify('주간보고를 삭제했습니다.') } catch (error) { notify(errorText(error, '보고서를 삭제할 수 없습니다.'), 'error') } finally { setBusy(false) } }
  const canSubmit = !report || report.status !== 'CLOSED'
  const revisionReason = revisionReasonOf(report)
  return <><PageHeader title="내 주간보고" description="이번 주 성과와 다음 주 계획을 업무 항목 단위로 기록합니다." action={<div className="header-actions">{report && <><Button variant="danger" onClick={removeReport} disabled={busy}>보고서 삭제</Button><Button variant="secondary" onClick={() => setPresenting(true)}>▶ 발표 모드</Button><Button variant="secondary" onClick={() => saveExport(report.id)}>PPTX 다운로드{captureCount > 0 ? ` (캡처 ${captureCount}장 포함)` : ''}</Button></>}<Button variant={dirty ? 'primary' : 'secondary'} onClick={save} disabled={busy}>{busy ? '저장 중…' : report?.status === 'CLOSED' ? '수정 저장' : '임시저장'}</Button>{canSubmit && <Button onClick={submit} disabled={busy}>{workflowEnabled ? '검토 요청' : '제출·확정'}</Button>}</div>} />
    {report && <div className="report-meta"><StatusBadge status={report.status}/><SourceBadge source={report.sourceType}/><span>{report.weekStart} 시작 주차</span><span>버전 {report.version}</span><span className={dirty ? 'save-state unsaved' : 'save-state'}>{dirty ? '저장하지 않은 변경 있음' : '저장됨'}</span>{report.status === 'REVISION_REQUESTED' && <strong>수정 후 다시 제출해 주세요.</strong>}</div>}
    {/* The reason, where the writer is standing.

        The meta line said 반려 의견을 확인하고 수정해 주세요 and stopped there.
        The reason itself is a comment, and the comment list is below seven work
        item forms and the quality check — measured on a seeded deployment, the
        notice sat at y=182 and the sentence it was pointing at sat at y=4562,
        nearly four screens down. The one moment in the review workflow where a
        writer needs to be told something, and the screen only told them that
        somewhere there was something to read. */}
    {/* Who else can already read this.

        A draft is unsubmitted, and a writer reasonably takes that to mean it is
        theirs until they hand it in. It is not: canViewReport has no status
        condition, so an administrator and any leader above them in the
        organisation tree can open it now — and the team list shows it with an
        열기 link beside the 작성 중 badge.

        That is a policy for the deployment to set, not something to change
        underneath one. What can be fixed without changing it is the surprise:
        say it where the writing happens. */}
    {report && report.status === 'DRAFT' && <p className="setting-help">
      작성 중인 보고서도 <strong>관리자와 상위 조직의 팀장은 지금 열어 볼 수 있습니다.</strong>
      제출은 검토를 요청하는 행위이지, 이 글이 처음 보이게 되는 시점이 아닙니다.
    </p>}
    {report && report.status === 'REVISION_REQUESTED' && <div className="revision-notice">
      <strong>반려 사유</strong>
      {revisionReason
        ? <><p>{revisionReason.content}</p>
            <small>{revisionReason.displayName} · {new Date(revisionReason.createdAt).toLocaleString('ko-KR')}</small></>
        : <p>사유가 남아 있지 않습니다. {report.reviewedBy
            ? <>반려한 <strong>{report.reviewedBy}</strong>님에게 확인해 주세요.</>
            : '검토자에게 직접 확인해 주세요.'}</p>}
    </div>}
    {/* An approval is a record of who took responsibility for the week, and the
        screen showed a time with nobody's name on it while the database had
        held the reviewer since the workflow existed. */}
    {report && report.status === 'APPROVED' && report.reviewedAt && <div className="revision-notice approved">
      <strong>승인</strong>
      <p>{report.reviewedBy ? `${report.reviewedBy}님이 승인했습니다.` : '승인되었습니다.'}
        {' '}<small>{new Date(report.reviewedAt).toLocaleString('ko-KR')}</small></p>
    </div>}
    {editable && candidateData?.enabled && <Card title="Confluence 자동 초안" action={<span className={`sync-chip ${candidateData.sync.status.toLowerCase()}`}>{candidateData.sync.status === 'RUNNING' ? '활동 수집 중' : candidateData.sync.lastSuccessAt ? `최근 수집 ${new Date(candidateData.sync.lastSuccessAt).toLocaleString('ko-KR')}` : '첫 수집 대기'}</span>}><p className="muted">Confluence에서 이번 주 작성·수정한 업무 문서를 자동으로 발견했습니다. 검색하거나 가져올 필요 없이, 내용을 확인한 뒤 보고서에 반영하세요.</p>{candidateData.sync.errorMessage && <div className="import-error">최근 동기화: {candidateData.sync.errorMessage}</div>}{candidateData.candidates.length ? <div className="confluence-candidates">{candidateData.candidates.map(candidate => { const applied = pendingCandidateIDs.includes(candidate.id); return <section key={candidate.id} className={applied ? 'applied' : ''}><header><input value={candidate.category} disabled={applied} onChange={e => changeCandidate(candidate.id, { category: e.target.value })} aria-label="업무 구분"/><input value={candidate.normalizedTitle} disabled={applied} onChange={e => changeCandidate(candidate.id, { normalizedTitle: e.target.value })} aria-label="업무 제목"/><span className={candidate.confidence < .6 ? 'confidence low' : 'confidence'}>신뢰도 {Math.round(candidate.confidence * 100)}%</span></header><div className="candidate-fields"><label>금주 실적<textarea value={candidate.currentResult} disabled={applied} onChange={e => changeCandidate(candidate.id, { currentResult: e.target.value })}/></label><label>차주 계획<textarea value={candidate.nextPlan} disabled={applied} onChange={e => changeCandidate(candidate.id, { nextPlan: e.target.value })}/></label><label>이슈<textarea value={candidate.issue} disabled={applied} onChange={e => changeCandidate(candidate.id, { issue: e.target.value })}/></label></div><details><summary>Confluence 출처 {candidate.sources.length}건</summary><ul>{candidate.sources.map(source => <li key={source.pageId}>{source.pageUrl ? <a href={source.pageUrl} target="_blank" rel="noreferrer">{source.title}</a> : <span>{source.title}</span>}<small>{source.spaceKey} · v{source.pageVersion} · {source.activityType === 'CREATED' ? '작성' : source.activityType === 'MODIFIED' ? '수정' : '작성·수정'}</small></li>)}</ul></details><footer><Button variant="ghost" onClick={() => ignoreCandidate(candidate)}>제외</Button><Button variant="secondary" onClick={() => saveCandidate(candidate)} disabled={applied}>초안 수정 저장</Button><Button onClick={() => applyCandidate(candidate)} disabled={applied}>{applied ? '보고서 반영됨' : '보고서에 반영'}</Button></footer></section>})}</div> : <Empty>{candidateData.sync.status === 'RUNNING' ? 'Confluence 활동을 분석하고 있습니다.' : !candidateData.sync.lastSuccessAt ? 'Confluence 수집이 아직 한 번도 완료되지 않았습니다. 첫 수집이 끝나면 이번 주에 작성·수정한 문서가 여기에 나타납니다.' : '이번 주에 발견된 Confluence 업무 후보가 없습니다.'}</Empty>}</Card>}
    {editable && aiEnabled && <Card title="AI로 주간보고 초안 만들기"><p className="muted">이번 주 한 일, 다음 주 할 일과 이슈를 자유롭게 붙여 넣으세요. AI 결과는 자동 저장되지 않으며 적용 전에 직접 수정할 수 있습니다.</p><><textarea className="ai-source-input" aria-label="AI가 분석할 주간업무 내용" value={aiText} onChange={e => setAIText(e.target.value)} placeholder={'금주\nAI Gateway 인증 개발 완료, OpenShift 배포 테스트\n\n다음주\n성능 개선 및 운영 배포 준비\n\n이슈\nAPI가 간헐적으로 Timeout 발생'} maxLength={200000}/><div className="ai-source-actions"><span>입력 {aiText.length.toLocaleString()}자 · 입력에 없는 내용은 생성하지 않도록 분석합니다.</span><Button variant="secondary" onClick={analyzeAI} disabled={aiBusy}>{aiBusy ? 'AI 분석 중…' : aiResult ? '다시 분석' : 'AI 분석'}</Button></div></>
      {aiResult && <div className="ai-preview"><div className="ai-preview-head"><div><strong>AI 분석 미리보기</strong><span>낮은 신뢰도 항목을 확인하고 직접 고친 뒤 적용하세요.</span></div><span className="confidence">날짜 신뢰도 {Math.round(aiResult.dateConfidence * 100)}%</span></div>{aiResult.warnings.length > 0 && <div className="import-warnings">{aiResult.warnings.map((warning, index) => <span key={index}>⚠ {warning}</span>)}</div>}<label className="ai-summary">추천 요약<textarea value={aiResult.summary} onChange={e => setAIResult({ ...aiResult, summary: e.target.value })}/></label><div className="ai-preview-list">{aiResult.reportItems.map((item, index) => <section key={index}><header><input value={item.category} onChange={e => changeAIItem(index, { category: e.target.value })} placeholder="구분"/><input value={item.title} onChange={e => changeAIItem(index, { title: e.target.value })} placeholder="업무 제목"/><span className={item.confidence < .6 ? 'confidence low' : 'confidence'}>신뢰도 {Math.round(item.confidence * 100)}%</span><button className="remove-button" onClick={() => setAIResult({ ...aiResult, reportItems: aiResult.reportItems.filter((_, i) => i !== index) })}>제외</button></header><div><label>금주 실적<textarea value={item.currentResult} onChange={e => changeAIItem(index, { currentResult: e.target.value })}/></label><label>차주 계획<textarea value={item.nextPlan} onChange={e => changeAIItem(index, { nextPlan: e.target.value })}/></label><label>이슈<textarea value={item.issue} onChange={e => changeAIItem(index, { issue: e.target.value })}/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} onChange={e => changeAIItem(index, { progress: Number(e.target.value) })}/></label></div></section>)}</div><div className="ai-apply-actions"><Button variant="secondary" onClick={() => setAIResult(undefined)}>미리보기 닫기</Button><Button variant="secondary" onClick={() => applyAI('replace')}>전체 교체</Button><Button onClick={() => applyAI('merge')}>기존 내용과 병합</Button></div></div>}
    </Card>}
    {report && (report.status === 'SUBMITTED' || report.status === 'APPROVED' || report.status === 'CLOSED') && <div className="edit-notice">{report.status === 'CLOSED' ? '확정된 보고서를 수정하면 작성 중 상태로 돌아갑니다. 다시 제출해야 확정됩니다.' : '제출·승인된 보고서를 수정하면 기존 검토 결과가 해제되고 작성 중 상태로 돌아갑니다.'}</div>}
    {(previousFailed || followUpsFailed) && <p className="capture-warning">{previousFailed && followUpsFailed ? '지난주 계획과 후속 조치를' : previousFailed ? '지난주 계획을' : '결정에 딸린 후속 조치를'} 불러오지 못했습니다. 이어받을 것이 없다는 뜻은 아니므로, 화면을 다시 열어 확인해 주십시오.</p>}
    {includedMaterialsFailed && <p className="capture-warning">팀원 주간보고 자료를 불러오지 못했습니다. 선택한 팀원이 없다는 뜻은 아닙니다. <button className="link-button" onClick={() => { void loadIncludedMaterials() }}>다시 시도</button></p>}
    <Card title="주간 요약"><AutoResizeTextarea className="summary-input" aria-label="주간 요약" placeholder="한 주의 핵심 성과와 맥락을 짧게 요약하세요." value={summary} onChange={e => setSummary(e.target.value)} disabled={!editable} maxLength={10000}/></Card>
    {effectiveMaterials.length > 0 && <Card title="팀원 주간보고 자료" action={<span className="muted-chip">{effectiveMaterials.length}명 선택</span>}>
      <IncludedReportMaterials materials={effectiveMaterials} heading={false}/>
    </Card>}
    {/* A clean report used to render nothing at all, so a writer with nothing
        wrong could not tell a check that passed from a check that never ran.
        The card is the only place that says the rules were applied, and it
        disappeared exactly when it had good news. */}
    {quality && quality.findings.length === 0 && quality.checked > 0 &&
      <Card title="보고 품질 점검" action={<span className="muted">{quality.checked}개 업무 확인</span>}>
        <p className="muted">규칙 점검에서 걸리는 것이 없습니다. 진척도 역행, 같은 계획 반복, 계획한 일의 빈 실적, 지속되는 이슈 네 가지를 봤습니다.</p>
      </Card>}
    {quality && quality.findings.length > 0 && <Card title="보고 품질 점검" action={<span className="muted">{quality.checked}개 업무 확인</span>}>
      <p className="muted">제출 전에 작성자가 먼저 보는 점검입니다. 규칙으로만 판단하며 내용을 고치지 않습니다.</p>
      <ul className="quality-findings">{quality.findings.map((finding, index) => <li key={index} className={finding.severity === 'WARN' ? 'warn' : ''}>
        <div><strong>{finding.title}</strong><small>{finding.severity === 'WARN' ? '확인 필요' : '참고'}</small></div>
        <p>{finding.message}</p>
      </li>)}</ul>
    </Card>}
    {openFollowUps.length > 0 && <Card title="결정에 딸린 후속 조치"
      action={<span className="muted">{overdueFollowUps > 0 ? `기한 지남 ${overdueFollowUps}건` : `${openFollowUps.length}건`}</span>}>
      <p className="muted">회의나 보고에서 정해져 기록된 후속 조치 중, 아직 이번 주 보고에 없는 것입니다. 이미 한 일이면 그대로 두고, 이번 주에 다룰 일이면 항목으로 추가하세요.</p>
      <ul className="carry-over">{openFollowUps.map(entry => <li key={entry.decisionId}>
        <div>
          <strong>{entry.workTitle}</strong>
          <small>{entry.decidedBy} 결정 · {entry.decidedOn}{entry.dueDate ? ` · 기한 ${entry.dueDate}` : ''}</small>
          <p>{entry.followUp}</p>
          {entry.overdue && <span className="state-chip stalled">기한 지남</span>}
        </div>
        <Button variant="secondary" onClick={() => carryFollowUp(entry)}>+ 업무 항목으로 추가</Button>
      </li>)}</ul>
    </Card>}
    {unreported.length > 0 && <Card title="지난주 계획 이어받기" action={<span className="muted">{previous?.weekStart} 보고 기준</span>}>
      <p className="muted">지난주에 계획했지만 이번 주 보고에 아직 없는 업무입니다. 끝난 일이면 그대로 두고, 이어서 하고 있다면 항목으로 추가하세요.</p>
      <ul className="carry-over">{unreported.map((plan, index) => <li key={index}><div><strong>{plan.title}</strong>{plan.category.trim() ? <small>{plan.category}</small> : null}<p>{plan.nextPlan}</p></div><Button variant="secondary" onClick={() => carryForward(plan)}>+ 업무 항목으로 추가</Button></li>)}</ul>
    </Card>}
    <div className="item-heading"><div><h2>업무 항목</h2><p>금주 실적, 차주 계획, 이슈를 한 단위로 관리합니다.</p></div>{editable && <Button variant="secondary" onClick={() => setItems([...items, blankItem()])}>+ 항목 추가</Button>}</div>
    {items.length ? items.map((item, index) => { const plan = previousFor(item); return <Card key={index} className="report-item" title={`업무 ${index + 1}`} action={editable && items.length > 1 ? <button className="remove-button" onClick={() => setItems(items.filter((_, i) => i !== index))}>삭제</button> : undefined}>
      <div className="form-grid title-grid"><label>구분<input value={item.category} disabled={!editable} maxLength={80} onChange={e => changeItem(index, { category: e.target.value })} placeholder="프로젝트 / 운영"/></label><label>업무 제목<input value={item.title} disabled={!editable} maxLength={240} onChange={e => changeItem(index, { title: e.target.value })} placeholder="업무명을 입력하세요"/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} disabled={!editable} onChange={e => changeItem(index, { progress: Number(e.target.value) })}/></label></div>
      {plan && <div className="previous-plan"><span>지난주 계획 · {previous?.weekStart}</span><p>{plan.nextPlan.trim() || '계획을 적지 않은 업무입니다.'}</p>{plan.issue.trim() ? <small>지난주 이슈 · {plan.issue}</small> : null}</div>}
      {/* The one moment anybody knows how an obstacle ended.
          The weekly rows already say an issue ran for N weeks; they cannot say
          whether the blank field this week means it was cleared or that the
          author stopped writing it down, and those are opposite facts. Asked
          once, here, and never again once answered — the alternative is a
          checklist that repeats a demand at somebody who already acted on it. */}
      {editable && plan && plan.issue.trim() && !item.issue.trim() && <div className="issue-ended">
        <div>
          <strong>지난주 이슈가 이번 주에는 비어 있습니다.</strong>
          <p>{plan.issue}</p>
        </div>
        {item.issueOutcome === 'RESOLVED'
          ? <span className="issue-ended-done">해소로 기록합니다 · 저장 시 반영</span>
          : <div className="issue-ended-actions">
              <Button variant="secondary" onClick={() => changeItem(index, { issueOutcome: 'RESOLVED' })}>해소됨</Button>
              <Button variant="ghost" onClick={() => changeItem(index, { issue: plan.issue, issueOutcome: undefined })}>아직 남음</Button>
            </div>}
      </div>}
      {/* Where the blockage is written, offer to say what it is waiting on.
          v0.40-42 built dependency links, bottlenecks and 타 조직 대기 on top of
          them. Measured on real data: 12 issues written, 0 links declared. The
          machinery is complete and structurally unreachable, because declaring
          a blocker lives on the work tracking screen and being blocked is
          written here. So the declaration comes to the sentence. */}
      {editable && item.workItemId && item.issue.trim() && <IssueDependency
        workItemId={item.workItemId} notify={notify} />}
      <div className="form-grid content-grid"><label>이번 주 — 한 일<AutoResizeTextarea value={item.currentResult} disabled={!editable} maxLength={20000} onChange={e => changeItem(index, { currentResult: e.target.value })} placeholder="완료한 일과 결과를 적으세요."/></label><label>다음 주 — 할 일<AutoResizeTextarea value={item.nextPlan} disabled={!editable} maxLength={20000} onChange={e => changeItem(index, { nextPlan: e.target.value })} placeholder="다음 주 계획을 적으세요."/></label><label>이슈 / 지원 요청<AutoResizeTextarea value={item.issue} disabled={!editable} maxLength={20000} onChange={e => changeItem(index, { issue: e.target.value })} placeholder="이슈가 없다면 비워 두세요."/></label><label>상위 조직 요청<AutoResizeTextarea value={item.managementAsk ?? ''} disabled={!editable} maxLength={5000} onChange={e => changeItem(index, { managementAsk: e.target.value })} placeholder="상위 조직의 결정이나 지원이 필요한 내용만 적으세요."/></label></div>
    </Card> }) : <Empty>업무 항목을 추가해 주세요.</Empty>}
    {report && <AttachmentPanel reportId={report.id} editable notify={notify} onCountChange={setCaptureCount} />}
    {/* Shown once there is something to discuss: a draft nobody has looked at
        yet has nothing to answer. The author could previously read 검토 의견 and
        not reply to it. */}
    {report && (report.comments.length > 0 || report.status !== 'DRAFT') &&
      <Card title="검토 의견">
        <ReportComments reportId={report.id} comments={report.comments} notify={notify}
          onPosted={load} placeholder="검토 의견에 답하거나 진행 상황을 알리세요." />
      </Card>}
    {/* Presents what is on screen, including unsaved edits: a report is often
        walked through immediately after being written. */}
    {presenting && report && <ReportPresentation label={`${report.weekStart} 주간보고`}
      report={{ ...report, summary, items: validItems(), includedMaterials: effectiveMaterials }}
      onClose={() => setPresenting(false)} />}
    {editable && !aiEnabled && <p className="muted page-footnote">AI 초안 기능은 관리자가 AI Gateway를 설정하면 이 화면에 나타납니다.</p>}
  </>
}

/**
 * The comparable content of a report. Sort order is left out because reordering
 * alone is not worth a warning, and blank rows are dropped so the empty item the
 * screen always offers does not read as unsaved work.
 */
function snapshotOf(summary: string, items: ReportItem[]): string {
  const filled = items.filter(item => item.title.trim() || item.currentResult.trim() || item.nextPlan.trim() || item.issue.trim() || (item.managementAsk ?? '').trim())
  return JSON.stringify([summary.trim(), filled.map(item => [item.category.trim(), item.title.trim(), item.currentResult.trim(), item.nextPlan.trim(), item.issue.trim(), (item.managementAsk ?? '').trim(), item.progress])])
}

function mergeReportItems(existing: ReportItem[], incoming: ReportItem[]): ReportItem[] {
  const result = existing.filter(item => item.title.trim()).map(item => ({ ...item }))
  for (const item of incoming) {
    const matched = result.find(candidate => candidate.category.trim().toLowerCase() === item.category.trim().toLowerCase() && candidate.title.trim().toLowerCase() === item.title.trim().toLowerCase())
    if (!matched) { result.push({ ...item, sortOrder: result.length }); continue }
    matched.currentResult = mergeText(matched.currentResult, item.currentResult)
    matched.nextPlan = mergeText(matched.nextPlan, item.nextPlan)
    matched.issue = mergeText(matched.issue, item.issue)
    matched.progress = Math.max(matched.progress, item.progress)
    matched.candidateId = matched.candidateId ?? item.candidateId
  }
  return result.map((item, index) => ({ ...item, sortOrder: index }))
}

function mergeText(existing: string, incoming: string) {
  const left = existing.trim(); const right = incoming.trim()
  if (!right || left.includes(right)) return left
  return left ? `${left}\n${right}` : right
}
