import { useEffect, useRef } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent, PropsWithChildren, ReactNode } from 'react'
import { isTopLayer, popLayer, pushLayer } from './layers'
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

export function Empty({ children, action }: PropsWithChildren<{ action?: ReactNode }>) {
  return <div className="empty"><span className="empty-icon">◇</span><p>{children}</p>{action && <div className="empty-action">{action}</div>}</div>
}

/**
 * A dialog that behaves like one: Escape closes it, focus moves into it when it
 * opens and returns where it came from when it closes, and Tab stays inside
 * while it is up.
 *
 * These were previously written inline on each screen, so closing a window
 * meant a different gesture depending on which screen you were on — the command
 * palette and presentation mode took Escape, the detail views did not.
 */
export function Modal({ onClose, beforeClose, label, className = '', children }: PropsWithChildren<{
  onClose: () => void
  /** Return false to keep the dialog open, for unsaved work worth a question. */
  beforeClose?: () => boolean
  label: string
  className?: string
}>) {
  const panel = useRef<HTMLDivElement>(null)
  // Read through a ref because every call site passes a fresh closure on each
  // render. With these in the dependency list the effect tore down and set up
  // again on every keystroke, and its setup moved focus to the dialog itself —
  // so the first character typed into a field was also the last.
  const handlers = useRef({ onClose, beforeClose })
  handlers.current = { onClose, beforeClose }
  const close = () => {
    if (handlers.current.beforeClose && !handlers.current.beforeClose()) return
    handlers.current.onClose()
  }

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    panel.current?.focus({ preventScroll: true })
    const id = pushLayer()
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        // Someone earlier in the dispatch already dealt with this keypress. The
        // layer check alone is not enough: React flushes state synchronously
        // during a keydown, so an overlay that closes itself has already left
        // the stack by the time this listener runs, and this dialog would find
        // itself on top and close too.
        if (event.defaultPrevented) return
        // Only the top layer answers, so leaving a deck opened above this
        // dialog does not also close the dialog underneath it.
        if (!isTopLayer(id)) return
        // A Korean input method uses Escape to cancel its candidate list; that
        // keypress must not also throw away the form.
        if (event.isComposing) return
        event.preventDefault()
        if (handlers.current.beforeClose && !handlers.current.beforeClose()) return
        handlers.current.onClose()
        return
      }
      if (event.key !== 'Tab' || !panel.current) return
      // Tab used to walk out of the dialog and into the page behind it, which
      // leaves a keyboard user editing something they cannot see.
      const stops = panel.current.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')
      if (!stops.length) return
      const first = stops[0]
      const last = stops[stops.length - 1]
      if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
      else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      popLayer(id)
      opener?.focus?.({ preventScroll: true })
    }
    // Opened once for the lifetime of the dialog; the handlers are read live.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  return <div className="modal-backdrop" onClick={close}>
    <div className={`modal ${className}`} ref={panel} tabIndex={-1} role="dialog" aria-modal="true" aria-label={label}
      onClick={event => event.stopPropagation()}>{children}</div>
  </div>
}
export function Spinner() { return <div className="spinner-wrap"><span className="spinner"/><span>불러오는 중…</span></div> }

export function Toast({ message, kind = 'success', onClose }: { message: string; kind?: 'success' | 'error'; onClose: () => void }) {
  // A toast used to stay until something replaced it. It sits at the bottom
  // right with a z-index above the page, so on the import screen it settled on
  // top of the confirm button and stayed there — a click aimed at the button
  // landed on the message instead. A success has said all it has to say; an
  // error may need reading twice, so that one waits for the reader. Either way
  // the message no longer swallows clicks meant for what is underneath it.
  const close = useRef(onClose)
  close.current = onClose
  useEffect(() => {
    if (kind === 'error') return
    const timer = window.setTimeout(() => close.current(), 5000)
    return () => window.clearTimeout(timer)
  }, [kind])
  return <div className={`toast ${kind}`} role="status">{message}<button onClick={onClose} aria-label="닫기">×</button></div>
}

export function formatDate(value?: string) { if (!value) return '-'; return new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeStyle: value.includes('T') ? 'short' : undefined }).format(new Date(value)) }

// openable makes a table row a control rather than a shape that happens to
// react to a mouse.
//
// Four screens put the only way into a detail on the row itself — 과거 보고,
// 팀 주간보고, 업무 추적, 기간 업무보고 — with no button inside it. Clicking
// worked; tabbing reached nothing, so a keyboard-only reader could open none of
// them. Measured in a browser: of the elements that draw a pointer cursor on
// those pages, none of the rows were focusable.
//
// Spread onto a <tr>: it becomes reachable by Tab, announced as a button, and
// answers Enter and Space the way one does. Space also scrolls a page, so its
// default is prevented — on a row that is the whole point of the key.
export function openable(open: () => void, label: string) {
  return {
    onClick: open,
    onKeyDown: (event: ReactKeyboardEvent) => {
      if (event.key !== 'Enter' && event.key !== ' ') return
      if (event.target !== event.currentTarget) return
      event.preventDefault()
      open()
    },
    tabIndex: 0,
    role: 'button' as const,
    'aria-label': label,
  }
}
