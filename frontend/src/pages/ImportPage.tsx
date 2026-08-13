import { useEffect, useMemo, useState } from 'react'
import { api, post } from '../api'
import { Button, Card, Empty, PageHeader, Spinner, formatDate } from '../components'
import type { AIReportItem, ImportFile, ImportJob } from '../types'

interface ImportDraft {
  id: number
  selected: boolean
  weekStart: string
  summary: string
  strategy: 'CREATE' | 'MERGE' | 'REPLACE' | 'SKIP'
  items: AIReportItem[]
}

export default function ImportPage({ aiEnabled, currentWeekStart, notify }: { aiEnabled: boolean; currentWeekStart: string; notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [jobs, setJobs] = useState<ImportJob[]>()
  const [selectedID, setSelectedID] = useState<number>()
  const [detail, setDetail] = useState<ImportJob>()
  const [files, setFiles] = useState<File[]>([])
  const [drafts, setDrafts] = useState<Record<number, ImportDraft>>({})
  const [busy, setBusy] = useState(false)
  const [dragging, setDragging] = useState(false)
  const processing = detail?.status === 'PENDING' || detail?.status === 'PROCESSING'

  const loadHistory = async () => { const values = await api<ImportJob[]>('/api/v1/import/history'); setJobs(values); if (!selectedID && values.length) setSelectedID(values[0].id) }
  const loadDetail = async (id: number) => { const value = await api<ImportJob>(`/api/v1/import/${id}`); setDetail(value); setDrafts(previous => { const next = { ...previous }; const jobFileIDs = new Set((value.files ?? []).map(file => file.id)); for (const file of value.files ?? []) { if ((file.status === 'READY' || file.status === 'NEEDS_REVIEW') && file.result && !next[file.id]) { const weekStart = file.detectedWeekStart || file.result.weekStart; const sameWeekSelected = Object.values(next).some(draft => jobFileIDs.has(draft.id) && draft.id !== file.id && draft.weekStart === weekStart && draft.selected && draft.strategy !== 'SKIP'); next[file.id] = draftFromFile(file, sameWeekSelected) } } return next }) }
  useEffect(() => { loadHistory().catch(() => setJobs([])) }, [])
  // Dropping a file anywhere outside the drop zone would make the browser open
  // it and navigate away from the page, discarding the review in progress.
  useEffect(() => {
    const swallow = (event: DragEvent) => { event.preventDefault() }
    window.addEventListener('dragover', swallow)
    window.addEventListener('drop', swallow)
    return () => { window.removeEventListener('dragover', swallow); window.removeEventListener('drop', swallow) }
  }, [])
  useEffect(() => { if (selectedID) loadDetail(selectedID) }, [selectedID])
  useEffect(() => { if (!processing || !selectedID) return; const timer = window.setInterval(() => { void Promise.all([loadHistory(), loadDetail(selectedID)]).catch(() => undefined) }, 2500); return () => window.clearInterval(timer) }, [processing, selectedID])

  // A drop carries no `accept` filtering, so the PPTX check has to happen here.
  const acceptFiles = (incoming: File[]) => {
    const accepted = incoming.filter(file => isPPTX(file))
    const rejected = incoming.length - accepted.length
    if (rejected > 0) notify(`PPTX가 아닌 파일 ${rejected}개는 제외했습니다.`, 'error')
    if (accepted.length) setFiles(accepted)
  }
  // Dropping mirrors the file picker: selection is always allowed and only the
  // upload button reacts to the AI Gateway being unavailable.
  const onDrop = (event: React.DragEvent) => {
    event.preventDefault(); event.stopPropagation(); setDragging(false)
    if (busy) return
    acceptFiles(Array.from(event.dataTransfer.files ?? []))
  }
  // The drop event only fires when the drag events before it are cancelled.
  const onDragOver = (event: React.DragEvent) => {
    event.preventDefault(); event.stopPropagation()
    event.dataTransfer.dropEffect = busy ? 'none' : 'copy'
    if (!dragging) setDragging(true)
  }
  const onDragLeave = (event: React.DragEvent) => {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return
    setDragging(false)
  }

  const upload = async () => { if (!files.length) return; setBusy(true); try { const body = new FormData(); files.forEach(file => body.append('files', file)); const result = await api<{ id: number }>('/api/v1/import/pptx', { method: 'POST', body }); setFiles([]); setSelectedID(result.id); await Promise.all([loadHistory(), loadDetail(result.id)]); notify(`${files.length}개 PPTX Import 분석을 시작했습니다.`) } catch (error) { notify(error instanceof Error ? error.message : 'PPTX를 업로드할 수 없습니다.', 'error') } finally { setBusy(false) } }
  const retry = async () => { if (!detail) return; setBusy(true); try { await post(`/api/v1/import/${detail.id}/analyze`); await loadDetail(detail.id); notify('실패한 파일의 재분석을 시작했습니다.') } catch (error) { notify(error instanceof Error ? error.message : '재분석할 수 없습니다.', 'error') } finally { setBusy(false) } }
  const reanalyzeFile = async (fileID: number) => { if (!detail || !confirm('현재 분석 편집값을 버리고 최신 파서와 AI로 다시 분석하시겠습니까?')) return; setBusy(true); try { setDrafts(current => { const next = { ...current }; delete next[fileID]; return next }); await post(`/api/v1/import/${detail.id}/analyze`, { fileIds: [fileID] }); await Promise.all([loadHistory(), loadDetail(detail.id)]); notify('선택한 PPTX를 최신 분석 파이프라인으로 다시 처리합니다.') } catch (error) { notify(error instanceof Error ? error.message : '파일을 재분석할 수 없습니다.', 'error') } finally { setBusy(false) } }
  const selectedDrafts = useMemo(() => Object.values(drafts).filter(draft => draft.selected && (detail?.files ?? []).some(file => file.id === draft.id && (file.status === 'READY' || file.status === 'NEEDS_REVIEW'))), [drafts, detail])
  const confirmImport = async () => { if (!detail || !selectedDrafts.length) { notify('확정할 파일을 선택하세요.', 'error'); return } if (selectedDrafts.some(draft => draft.strategy !== 'SKIP' && (!draft.weekStart || !draft.items.some(item => item.title.trim())))) { notify('주차와 업무 항목 제목을 확인하세요.', 'error'); return } if (selectedDrafts.some(draft => draft.strategy !== 'SKIP' && !sameWeekday(draft.weekStart, currentWeekStart))) { notify(`보고 주차는 관리자 기준 ${weekdayName(currentWeekStart)}요일로 선택하세요.`, 'error'); return } if (!confirm(`${selectedDrafts.length}개 과거 보고서를 저장하시겠습니까?`)) return; const files = selectedDrafts.map(draft => ({ id: draft.id, weekStart: draft.weekStart, summary: draft.summary, strategy: draft.strategy, items: draft.items.map(item => ({ category: item.category, title: item.title, currentResult: item.currentResult, nextPlan: item.nextPlan, issue: item.issue, progress: item.progress, confidence: item.confidence })) })); setBusy(true); try { await post(`/api/v1/import/${detail.id}/confirm`, { files }); await Promise.all([loadHistory(), loadDetail(detail.id)]); notify('선택한 과거 주간보고를 저장했습니다.') } catch (error) { notify(error instanceof Error ? error.message : 'Import를 확정할 수 없습니다.', 'error') } finally { setBusy(false) } }
  const updateDraft = (id: number, patch: Partial<ImportDraft>) => setDrafts(current => ({ ...current, [id]: { ...current[id], ...patch } }))
  const updateItem = (fileID: number, index: number, patch: Partial<AIReportItem>) => { const draft = drafts[fileID]; if (!draft) return; updateDraft(fileID, { items: draft.items.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) }) }

  return <><PageHeader title="과거 PPTX 가져오기" description="기존 주간보고를 OpenXML 파서와 AI로 분석한 뒤 검토·확정하여 과거 데이터로 적재합니다."/>
    <Card title="PPTX 다중 업로드"><p className="muted">파일명만 신뢰하지 않고 슬라이드 텍스트·표 셀·날짜 범위를 먼저 추출합니다. 분석 결과는 확정 전까지 실제 보고서에 저장되지 않습니다.</p>{!aiEnabled && <div className="ai-disabled">AI Gateway가 비활성화되어 있어 새 파일을 분석할 수 없습니다. 관리자 설정 후 업로드하세요.</div>}<div className="import-upload"><label className={`file-drop ${dragging ? 'dragging' : ''}`} onDrop={onDrop} onDragOver={onDragOver} onDragEnter={onDragOver} onDragLeave={onDragLeave}><input type="file" multiple accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation" onChange={event => { acceptFiles(Array.from(event.target.files ?? [])); event.target.value = '' }}/><strong>{dragging ? '여기에 놓으면 파일이 추가됩니다' : files.length ? `${files.length}개 파일 선택됨` : 'PPTX 파일을 선택하거나 여기로 끌어다 놓으세요'}</strong><span>{files.length ? files.map(file => file.name).join(' · ') : '동일 SHA-256 파일은 중복으로 표시되며 다시 적재되지 않습니다.'}</span>{files.length > 0 && <button type="button" className="link-button" onClick={event => { event.preventDefault(); setFiles([]) }}>선택 해제</button>}</label><Button onClick={upload} disabled={!aiEnabled || !files.length || busy}>{busy ? '처리 중…' : '업로드·분석 시작'}</Button></div></Card>
    <div className="import-layout"><Card title="Import 이력">{jobs === undefined ? <Spinner/> : !jobs.length ? <Empty>아직 Import 작업이 없습니다.</Empty> : <div className="import-job-list">{jobs.map(job => <button key={job.id} className={selectedID === job.id ? 'active' : ''} onClick={() => { setDetail(undefined); setSelectedID(job.id) }}><span><strong>작업 #{job.id}</strong><small>{formatDate(job.createdAt)}</small></span><span className={`import-status ${job.status.toLowerCase()}`}>{statusName(job.status)}</span><small>{job.processedFiles}/{job.totalFiles} · 실패 {job.failedFiles}</small></button>)}</div>}</Card>
      <div>{selectedID && !detail ? <Spinner/> : detail ? <><Card title={`작업 #${detail.id} · ${statusName(detail.status)}`} action={detail.failedFiles > 0 && !processing ? <Button variant="secondary" onClick={retry} disabled={busy}>실패 파일 재분석</Button> : undefined}><div className="import-progress"><div><span style={{ width: `${detail.totalFiles ? Math.round(detail.processedFiles / detail.totalFiles * 100) : 0}%` }}/></div><strong>{detail.processedFiles} / {detail.totalFiles}</strong></div>{processing && <p className="muted">백그라운드에서 슬라이드 구조를 보존해 파싱하고 AI 결과를 근거와 함께 검증하고 있습니다. 이 화면은 자동 갱신됩니다.</p>}</Card>{(detail.files ?? []).map(file => <ImportFileCard key={file.id} file={file} draft={drafts[file.id]} busy={busy} reanalyze={() => reanalyzeFile(file.id)} updateDraft={patch => updateDraft(file.id, patch)} updateItem={(index, patch) => updateItem(file.id, index, patch)}/>) }{selectedDrafts.length > 0 && <div className="import-confirm-bar"><span><strong>{selectedDrafts.length}개</strong> 파일 선택 · 저장 전 날짜, 업무 구분과 원본 슬라이드를 마지막으로 확인하세요.</span><Button onClick={confirmImport} disabled={busy}>{busy ? '저장 중…' : '선택 보고서 확정 저장'}</Button></div>}</> : <Empty>왼쪽에서 Import 작업을 선택하세요.</Empty>}</div>
    </div>
  </>
}

function ImportFileCard({ file, draft, busy, reanalyze, updateDraft, updateItem }: { file: ImportFile; draft?: ImportDraft; busy: boolean; reanalyze: () => void; updateDraft: (patch: Partial<ImportDraft>) => void; updateItem: (index: number, patch: Partial<AIReportItem>) => void }) {
  const editable = (file.status === 'READY' || file.status === 'NEEDS_REVIEW') && draft
  return <Card className={`import-file ${file.status === 'NEEDS_REVIEW' ? 'needs-review' : ''}`} title={file.originalFilename} action={<span className={`import-status ${file.status.toLowerCase()}`}>{fileStatusName(file.status)}</span>}><div className="import-file-meta"><span>크기 {(file.sizeBytes / 1024).toFixed(1)} KB</span><span>SHA {file.fileHash.slice(0, 12)}…</span>{file.dateSource && <span>날짜 근거 {file.dateSource}</span>}{file.confidence > 0 && <span className={file.confidence < .75 ? 'confidence low' : 'confidence'}>날짜 신뢰도 {Math.round(file.confidence * 100)}%</span>}{(file.status === 'READY' || file.status === 'NEEDS_REVIEW' || file.status === 'FAILED') && <button className="link-button" onClick={reanalyze} disabled={busy}>최신 파이프라인으로 다시 분석</button>}</div>{file.errorMessage && <div className="import-error">{file.errorMessage}</div>}{file.status === 'DUPLICATE' && <p className="muted">기존 Import 파일 #{file.duplicateOf}와 내용이 같아 분석과 저장을 생략했습니다.</p>}{file.reportId && <p className="import-linked">저장된 보고서 #{file.reportId}에 연결되었습니다.</p>}{file.status === 'NEEDS_REVIEW' && <div className="review-required">낮은 신뢰도 결과는 기본 선택하지 않습니다. 날짜·업무 구분·원본 슬라이드를 확인한 뒤 직접 선택하세요.</div>}{editable && <><div className="import-decision"><label className="checkbox-line"><input type="checkbox" checked={draft.selected} onChange={event => updateDraft({ selected: event.target.checked })}/> 이 파일을 과거 보고서로 저장</label><label>보고 주차<input type="date" value={draft.weekStart} onChange={event => updateDraft({ weekStart: event.target.value })}/></label><label>중복 주차 처리<select value={draft.strategy} onChange={event => updateDraft({ strategy: event.target.value as ImportDraft['strategy'] })}><option value="CREATE">신규 생성</option><option value="MERGE">기존 보고서와 병합</option><option value="REPLACE">기존 보고서 교체</option><option value="SKIP">건너뛰기</option></select></label>{file.conflictReportId && <span className="conflict-warning">동일 주차 보고서 #{file.conflictReportId} ({file.conflictReportStatus})가 있습니다.</span>}</div>{file.result?.warnings.length ? <div className="import-warnings">{file.result.warnings.map((warning, index) => <span key={index}>⚠ {warning}</span>)}</div> : null}<label className="import-summary">주간 요약<textarea value={draft.summary} onChange={event => updateDraft({ summary: event.target.value })}/></label><div className="import-item-editor">{draft.items.map((item, index) => <section key={index}><header><input value={item.category} onChange={event => updateItem(index, { category: event.target.value })} placeholder="구분"/><input value={item.title} onChange={event => updateItem(index, { title: event.target.value })} placeholder="업무 제목"/><span className={item.confidence < .6 ? 'confidence low' : 'confidence'}>{Math.round(item.confidence * 100)}%</span><div className="item-controls"><button disabled={index === 0} onClick={() => updateDraft({ items: moveItem(draft.items, index, index - 1) })}>↑</button><button disabled={index === draft.items.length - 1} onClick={() => updateDraft({ items: moveItem(draft.items, index, index + 1) })}>↓</button><button className="remove-button" onClick={() => updateDraft({ items: draft.items.filter((_, itemIndex) => itemIndex !== index) })}>제외</button></div></header>{(item.sourceSlides?.length || item.categoryConfidence !== undefined) && <small className="import-origin">{item.sourceSlides?.length ? `원본 슬라이드 ${item.sourceSlides.join(', ')}` : '원본 슬라이드 미확인'}{item.categoryConfidence !== undefined ? ` · 구분 신뢰도 ${Math.round(item.categoryConfidence * 100)}%` : ''}</small>}<div><label>금주 실적<textarea value={item.currentResult} onChange={event => updateItem(index, { currentResult: event.target.value })}/></label><label>차주 계획<textarea value={item.nextPlan} onChange={event => updateItem(index, { nextPlan: event.target.value })}/></label><label>이슈<textarea value={item.issue} onChange={event => updateItem(index, { issue: event.target.value })}/></label><label>진척도 <b>{item.progress}%</b><input type="range" min="0" max="100" step="5" value={item.progress} onChange={event => updateItem(index, { progress: Number(event.target.value) })}/></label></div></section>)}</div><div className="ai-apply-actions"><Button variant="secondary" onClick={() => updateDraft({ items: [...draft.items, { category: '', title: '', currentResult: '', nextPlan: '', issue: '', progress: 0, confidence: 1, categoryConfidence: 1, sourceSlides: [] }] })}>+ 업무 항목 추가</Button></div></>}</Card>
}

function draftFromFile(file: ImportFile, sameWeekSelected = false): ImportDraft {
  return { id: file.id, selected: file.status === 'READY', weekStart: file.detectedWeekStart || file.result?.weekStart || '', summary: file.result?.summary ?? '', strategy: file.conflictReportId || sameWeekSelected ? 'MERGE' : 'CREATE', items: file.result?.reportItems ?? [] }
}

const pptxMimeType = 'application/vnd.openxmlformats-officedocument.presentationml.presentation'
// Windows and some browsers report an empty or generic type for a dropped file,
// so the extension is the reliable signal and the MIME type only confirms it.
function isPPTX(file: File) {
  return file.name.toLowerCase().endsWith('.pptx') || file.type === pptxMimeType
}

function moveItem(items: AIReportItem[], from: number, to: number) { if (to < 0 || to >= items.length || from === to) return items; const next = [...items]; const [item] = next.splice(from, 1); next.splice(to, 0, item); return next }
function weekday(value: string) { const date = new Date(`${value}T00:00:00Z`); return Number.isNaN(date.getTime()) ? -1 : date.getUTCDay() }
function sameWeekday(left: string, right: string) { return weekday(left) >= 0 && weekday(left) === weekday(right) }
function weekdayName(value: string) { return ['일','월','화','수','목','금','토'][weekday(value)] ?? '' }

function statusName(status: ImportJob['status']) { return ({ PENDING: '대기', PROCESSING: '분석 중', READY: '검토 가능', PARTIAL: '일부 실패', FAILED: '실패', CONFIRMED: '확정 완료' } as Record<string, string>)[status] ?? status }
function fileStatusName(status: ImportFile['status']) { return ({ QUEUED: '대기', PROCESSING: '분석 중', READY: '정상', NEEDS_REVIEW: '확인 필요', FAILED: '실패', DUPLICATE: '중복', CONFIRMED: '저장 완료', SKIPPED: '건너뜀' } as Record<string, string>)[status] ?? status }
