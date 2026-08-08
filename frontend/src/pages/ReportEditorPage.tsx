import { useEffect, useState } from 'react'
import { api, post, put } from '../api'
import { Button, Card, Empty, PageHeader, SourceBadge, Spinner, StatusBadge } from '../components'
import type { AIWeeklyResult, Report, ReportItem } from '../types'

const blankItem = (): ReportItem => ({ category: '', title: '', currentResult: '', nextPlan: '', issue: '', progress: 0, sortOrder: 0 })

export default function ReportEditorPage({ workflowEnabled, aiEnabled, notify }: { workflowEnabled: boolean; aiEnabled: boolean; notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [report, setReport] = useState<Report | null>()
  const [summary, setSummary] = useState('')
  const [items, setItems] = useState<ReportItem[]>([blankItem()])
  const [busy, setBusy] = useState(false)
  const [aiText, setAIText] = useState('')
  const [aiResult, setAIResult] = useState<AIWeeklyResult>()
  const [aiBusy, setAIBusy] = useState(false)
  const [aiApplied, setAIApplied] = useState(false)
  const load = async () => { const value = await api<Report | null>('/api/v1/reports/current'); setReport(value); if (value) { setSummary(value.summary); setItems(value.items.length ? value.items : [blankItem()]); setAIApplied(value.sourceType === 'AI_TEXT') } }
  useEffect(() => { load() }, [])
  if (report === undefined) return <Spinner />
  const editable = !report || report.status === 'DRAFT' || report.status === 'REVISION_REQUESTED'
  const changeItem = (index: number, patch: Partial<ReportItem>) => setItems(items.map((item, i) => i === index ? { ...item, ...patch } : item))
  const validItems = () => items.filter(item => item.title.trim())
  const save = async () => { if (!validItems().length) { notify('업무 항목을 하나 이상 입력하세요.', 'error'); return false } setBusy(true); try { const payload = { summary, sourceType: aiApplied ? 'AI_TEXT' : 'MANUAL', items: validItems() }; if (report) await put(`/api/v1/reports/${report.id}`, { ...payload, version: report.version }); else await post('/api/v1/reports', payload); await load(); notify('보고서를 저장했습니다.'); return true } catch (error) { notify(error instanceof Error ? error.message : '저장할 수 없습니다.', 'error'); return false } finally { setBusy(false) } }
  const analyzeAI = async () => { if (!aiText.trim()) { notify('AI가 분석할 주간업무 내용을 입력하세요.', 'error'); return } setAIBusy(true); try { const result = await post<AIWeeklyResult>('/api/v1/ai/reports/parse-text', { text: aiText }); setAIResult(result); notify(`${result.reportItems.length}개 업무 항목을 구조화했습니다.`) } catch (error) { notify(error instanceof Error ? error.message : 'AI 분석을 완료할 수 없습니다.', 'error') } finally { setAIBusy(false) } }
  const changeAIItem = (index: number, patch: Partial<AIWeeklyResult['reportItems'][number]>) => setAIResult(aiResult ? { ...aiResult, reportItems: aiResult.reportItems.map((item, i) => i === index ? { ...item, ...patch } : item) } : undefined)
  const applyAI = (mode: 'merge' | 'replace') => { if (!aiResult) return; const incoming = aiResult.reportItems.filter(item => item.title.trim()).map((item, index) => ({ category: item.category, title: item.title, currentResult: item.currentResult, nextPlan: item.nextPlan, issue: item.issue, progress: item.progress, sortOrder: index })); if (!incoming.length) { notify('적용할 AI 업무 항목이 없습니다.', 'error'); return } setItems(mode === 'replace' ? incoming : mergeReportItems(validItems(), incoming)); if (aiResult.summary && (mode === 'replace' || !summary.trim())) setSummary(aiResult.summary); setAIApplied(true); setAIResult(undefined); notify(mode === 'merge' ? 'AI 결과를 기존 보고서와 병합했습니다. 저장 전 내용을 확인하세요.' : 'AI 결과로 업무 항목을 교체했습니다. 저장 전 내용을 확인하세요.') }
  const submit = async () => { let current = report; if (!current) { const saved = await save(); if (!saved) return; current = await api<Report | null>('/api/v1/reports/current') } else { const saved = await save(); if (!saved) return; current = await api<Report | null>('/api/v1/reports/current') } if (!current || !confirm(workflowEnabled ? '팀장 검토를 위해 제출하시겠습니까?' : '제출하면 바로 확정됩니다. 계속하시겠습니까?')) return; setBusy(true); try { await post(`/api/v1/reports/${current.id}/submit`); await load(); notify(workflowEnabled ? '보고서를 제출했습니다.' : '보고서를 확정했습니다.') } catch (error) { notify(error instanceof Error ? error.message : '제출할 수 없습니다.', 'error') } finally { setBusy(false) } }
  return <><PageHeader title="내 주간보고" description="이번 주 성과와 다음 주 계획을 업무 항목 단위로 기록합니다." action={<div className="header-actions">{report && <a className="button secondary" href={`/api/v1/reports/${report.id}/export.pptx`}>PPTX 다운로드</a>}{editable && <><Button variant="secondary" onClick={save} disabled={busy}>임시저장</Button><Button onClick={submit} disabled={busy}>{workflowEnabled ? '검토 요청' : '제출·확정'}</Button></>}</div>} />
    {report && <div className="report-meta"><StatusBadge status={report.status}/><SourceBadge source={report.sourceType}/><span>{report.weekStart} 시작 주차</span><span>버전 {report.version}</span>{report.status === 'REVISION_REQUESTED' && <strong>반려 의견을 확인하고 수정해 주세요.</strong>}</div>}
    {editable && <Card title="AI로 주간보고 초안 만들기"><p className="muted">이번 주 한 일, 다음 주 할 일과 이슈를 자유롭게 붙여 넣으세요. AI 결과는 자동 저장되지 않으며 적용 전에 직접 수정할 수 있습니다.</p>{!aiEnabled ? <div className="ai-disabled">AI 기능이 비활성화되어 있습니다. 관리자에게 AI Gateway 설정을 요청하세요.</div> : <><textarea className="ai-source-input" value={aiText} onChange={e => setAIText(e.target.value)} placeholder={'금주\nAI Gateway 인증 개발 완료, OpenShift 배포 테스트\n\n다음주\n성능 개선 및 운영 배포 준비\n\n이슈\nAPI가 간헐적으로 Timeout 발생'} maxLength={200000}/><div className="ai-source-actions"><span>입력 {aiText.length.toLocaleString()}자 · 입력에 없는 내용은 생성하지 않도록 분석합니다.</span><Button variant="secondary" onClick={analyzeAI} disabled={aiBusy}>{aiBusy ? 'AI 분석 중…' : aiResult ? '다시 분석' : 'AI 분석'}</Button></div></>}
      {aiResult && <div className="ai-preview"><div className="ai-preview-head"><div><strong>AI 분석 미리보기</strong><span>낮은 신뢰도 항목을 확인하고 직접 고친 뒤 적용하세요.</span></div><span className="confidence">날짜 신뢰도 {Math.round(aiResult.dateConfidence * 100)}%</span></div>{aiResult.warnings.length > 0 && <div className="import-warnings">{aiResult.warnings.map((warning, index) => <span key={index}>⚠ {warning}</span>)}</div>}<label className="ai-summary">추천 요약<textarea value={aiResult.summary} onChange={e => setAIResult({ ...aiResult, summary: e.target.value })}/></label><div className="ai-preview-list">{aiResult.reportItems.map((item, index) => <section key={index}><header><input value={item.category} onChange={e => changeAIItem(index, { category: e.target.value })} placeholder="구분"/><input value={item.title} onChange={e => changeAIItem(index, { title: e.target.value })} placeholder="업무 제목"/><span className={item.confidence < .6 ? 'confidence low' : 'confidence'}>신뢰도 {Math.round(item.confidence * 100)}%</span><button className="remove-button" onClick={() => setAIResult({ ...aiResult, reportItems: aiResult.reportItems.filter((_, i) => i !== index) })}>제외</button></header><div><label>금주 실적<textarea value={item.currentResult} onChange={e => changeAIItem(index, { currentResult: e.target.value })}/></label><label>차주 계획<textarea value={item.nextPlan} onChange={e => changeAIItem(index, { nextPlan: e.target.value })}/></label><label>이슈<textarea value={item.issue} onChange={e => changeAIItem(index, { issue: e.target.value })}/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} onChange={e => changeAIItem(index, { progress: Number(e.target.value) })}/></label></div></section>)}</div><div className="ai-apply-actions"><Button variant="secondary" onClick={() => setAIResult(undefined)}>미리보기 닫기</Button><Button variant="secondary" onClick={() => applyAI('replace')}>전체 교체</Button><Button onClick={() => applyAI('merge')}>기존 내용과 병합</Button></div></div>}
    </Card>}
    <Card title="주간 요약"><textarea className="summary-input" placeholder="한 주의 핵심 성과와 맥락을 짧게 요약하세요." value={summary} onChange={e => setSummary(e.target.value)} disabled={!editable} maxLength={10000}/></Card>
    <div className="item-heading"><div><h2>업무 항목</h2><p>금주 실적, 차주 계획, 이슈를 한 단위로 관리합니다.</p></div>{editable && <Button variant="secondary" onClick={() => setItems([...items, blankItem()])}>+ 항목 추가</Button>}</div>
    {items.length ? items.map((item, index) => <Card key={index} className="report-item" title={`업무 ${index + 1}`} action={editable && items.length > 1 ? <button className="remove-button" onClick={() => setItems(items.filter((_, i) => i !== index))}>삭제</button> : undefined}>
      <div className="form-grid title-grid"><label>구분<input value={item.category} disabled={!editable} onChange={e => changeItem(index, { category: e.target.value })} placeholder="프로젝트 / 운영"/></label><label>업무 제목<input value={item.title} disabled={!editable} onChange={e => changeItem(index, { title: e.target.value })} placeholder="업무명을 입력하세요"/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} disabled={!editable} onChange={e => changeItem(index, { progress: Number(e.target.value) })}/></label></div>
      <div className="form-grid content-grid"><label>이번 주 — 한 일<textarea value={item.currentResult} disabled={!editable} onChange={e => changeItem(index, { currentResult: e.target.value })} placeholder="완료한 일과 결과를 적으세요."/></label><label>다음 주 — 할 일<textarea value={item.nextPlan} disabled={!editable} onChange={e => changeItem(index, { nextPlan: e.target.value })} placeholder="다음 주 계획을 적으세요."/></label><label>이슈 / 지원 요청<textarea value={item.issue} disabled={!editable} onChange={e => changeItem(index, { issue: e.target.value })} placeholder="이슈가 없다면 비워 두세요."/></label></div>
    </Card>) : <Empty>업무 항목을 추가해 주세요.</Empty>}
    {report?.comments.length ? <Card title="검토 의견"><div className="comments">{report.comments.map(comment => <div key={comment.id}><strong>{comment.displayName}</strong><span>{new Date(comment.createdAt).toLocaleString('ko-KR')}</span><p>{comment.content}</p></div>)}</div></Card> : null}
  </>
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
  }
  return result.map((item, index) => ({ ...item, sortOrder: index }))
}

function mergeText(existing: string, incoming: string) {
  const left = existing.trim(); const right = incoming.trim()
  if (!right || left.includes(right)) return left
  return left ? `${left}\n${right}` : right
}
