import type { PropsWithChildren, ReactNode } from 'react'
import type { ReportSource, ReportStatus } from './types'

export function Button({ children, variant = 'primary', className = '', ...props }: PropsWithChildren<React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'secondary' | 'danger' | 'ghost' }>) {
  return <button className={`button ${variant} ${className}`} {...props}>{children}</button>
}

export function Card({ title, action, children, className = '' }: PropsWithChildren<{ title?: string; action?: ReactNode; className?: string }>) {
  return <section className={`card ${className}`}>{(title || action) && <header className="card-head"><h2>{title}</h2>{action}</header>}<div className="card-body">{children}</div></section>
}

export function PageHeader({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return <div className="page-header"><div><h1>{title}</h1>{description && <p>{description}</p>}</div>{action}</div>
}

const statusNames: Record<ReportStatus, string> = { DRAFT: '작성 중', SUBMITTED: '검토 대기', REVISION_REQUESTED: '반려/수정', APPROVED: '승인', CLOSED: '확정' }
export function StatusBadge({ status }: { status: ReportStatus }) { return <span className={`badge status-${status.toLowerCase()}`}>{statusNames[status]}</span> }
const sourceNames: Record<ReportSource, string> = { MANUAL: '직접 작성', AI_TEXT: 'AI 초안', PPTX_IMPORT: 'PPTX 가져오기', CONFLUENCE_AI: 'Confluence 자동 초안', CLONED: '보고서 복제', API: 'API', JIRA: 'Jira' }
export function SourceBadge({ source }: { source: ReportSource }) { return <span className={`badge source-${source.toLowerCase()}`}>{sourceNames[source]}</span> }

export function Empty({ children }: PropsWithChildren) { return <div className="empty"><span className="empty-icon">◇</span><p>{children}</p></div> }
export function Spinner() { return <div className="spinner-wrap"><span className="spinner"/><span>불러오는 중…</span></div> }

export function Toast({ message, kind = 'success', onClose }: { message: string; kind?: 'success' | 'error'; onClose: () => void }) {
  return <div className={`toast ${kind}`} role="status">{message}<button onClick={onClose} aria-label="닫기">×</button></div>
}

export function formatDate(value?: string) { if (!value) return '-'; return new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeStyle: value.includes('T') ? 'short' : undefined }).format(new Date(value)) }
