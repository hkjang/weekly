import { useEffect, useState } from 'react'
import { api, post, put } from '../api'
import { Button, Card, Empty, PageHeader, Spinner, StatusBadge } from '../components'
import type { Report, ReportItem } from '../types'

const blankItem = (): ReportItem => ({ category: '', title: '', currentResult: '', nextPlan: '', issue: '', progress: 0, sortOrder: 0 })

export default function ReportEditorPage({ workflowEnabled, notify }: { workflowEnabled: boolean; notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [report, setReport] = useState<Report | null>()
  const [summary, setSummary] = useState('')
  const [items, setItems] = useState<ReportItem[]>([blankItem()])
  const [busy, setBusy] = useState(false)
  const load = async () => { const value = await api<Report | null>('/api/v1/reports/current'); setReport(value); if (value) { setSummary(value.summary); setItems(value.items.length ? value.items : [blankItem()]) } }
  useEffect(() => { load() }, [])
  if (report === undefined) return <Spinner />
  const editable = !report || report.status === 'DRAFT' || report.status === 'REVISION_REQUESTED'
  const changeItem = (index: number, patch: Partial<ReportItem>) => setItems(items.map((item, i) => i === index ? { ...item, ...patch } : item))
  const validItems = () => items.filter(item => item.title.trim())
  const save = async () => { if (!validItems().length) { notify('업무 항목을 하나 이상 입력하세요.', 'error'); return false } setBusy(true); try { if (report) await put(`/api/v1/reports/${report.id}`, { summary, version: report.version, items: validItems() }); else await post('/api/v1/reports', { summary, items: validItems() }); await load(); notify('보고서를 저장했습니다.'); return true } catch (error) { notify(error instanceof Error ? error.message : '저장할 수 없습니다.', 'error'); return false } finally { setBusy(false) } }
  const submit = async () => { let current = report; if (!current) { const saved = await save(); if (!saved) return; current = await api<Report | null>('/api/v1/reports/current') } else { const saved = await save(); if (!saved) return; current = await api<Report | null>('/api/v1/reports/current') } if (!current || !confirm(workflowEnabled ? '팀장 검토를 위해 제출하시겠습니까?' : '제출하면 바로 확정됩니다. 계속하시겠습니까?')) return; setBusy(true); try { await post(`/api/v1/reports/${current.id}/submit`); await load(); notify(workflowEnabled ? '보고서를 제출했습니다.' : '보고서를 확정했습니다.') } catch (error) { notify(error instanceof Error ? error.message : '제출할 수 없습니다.', 'error') } finally { setBusy(false) } }
  return <><PageHeader title="내 주간보고" description="이번 주 성과와 다음 주 계획을 업무 항목 단위로 기록합니다." action={<div className="header-actions">{report && <a className="button secondary" href={`/api/v1/reports/${report.id}/export.pptx`}>PPTX 다운로드</a>}{editable && <><Button variant="secondary" onClick={save} disabled={busy}>임시저장</Button><Button onClick={submit} disabled={busy}>{workflowEnabled ? '검토 요청' : '제출·확정'}</Button></>}</div>} />
    {report && <div className="report-meta"><StatusBadge status={report.status}/><span>{report.weekStart} 시작 주차</span><span>버전 {report.version}</span>{report.status === 'REVISION_REQUESTED' && <strong>반려 의견을 확인하고 수정해 주세요.</strong>}</div>}
    <Card title="주간 요약"><textarea className="summary-input" placeholder="한 주의 핵심 성과와 맥락을 짧게 요약하세요." value={summary} onChange={e => setSummary(e.target.value)} disabled={!editable} maxLength={10000}/></Card>
    <div className="item-heading"><div><h2>업무 항목</h2><p>금주 실적, 차주 계획, 이슈를 한 단위로 관리합니다.</p></div>{editable && <Button variant="secondary" onClick={() => setItems([...items, blankItem()])}>+ 항목 추가</Button>}</div>
    {items.length ? items.map((item, index) => <Card key={index} className="report-item" title={`업무 ${index + 1}`} action={editable && items.length > 1 ? <button className="remove-button" onClick={() => setItems(items.filter((_, i) => i !== index))}>삭제</button> : undefined}>
      <div className="form-grid title-grid"><label>구분<input value={item.category} disabled={!editable} onChange={e => changeItem(index, { category: e.target.value })} placeholder="프로젝트 / 운영"/></label><label>업무 제목<input value={item.title} disabled={!editable} onChange={e => changeItem(index, { title: e.target.value })} placeholder="업무명을 입력하세요"/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} disabled={!editable} onChange={e => changeItem(index, { progress: Number(e.target.value) })}/></label></div>
      <div className="form-grid content-grid"><label>이번 주 — 한 일<textarea value={item.currentResult} disabled={!editable} onChange={e => changeItem(index, { currentResult: e.target.value })} placeholder="완료한 일과 결과를 적으세요."/></label><label>다음 주 — 할 일<textarea value={item.nextPlan} disabled={!editable} onChange={e => changeItem(index, { nextPlan: e.target.value })} placeholder="다음 주 계획을 적으세요."/></label><label>이슈 / 지원 요청<textarea value={item.issue} disabled={!editable} onChange={e => changeItem(index, { issue: e.target.value })} placeholder="이슈가 없다면 비워 두세요."/></label></div>
    </Card>) : <Empty>업무 항목을 추가해 주세요.</Empty>}
    {report?.comments.length ? <Card title="검토 의견"><div className="comments">{report.comments.map(comment => <div key={comment.id}><strong>{comment.displayName}</strong><span>{new Date(comment.createdAt).toLocaleString('ko-KR')}</span><p>{comment.content}</p></div>)}</div></Card> : null}
  </>
}
