import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { PresentSlide } from './PresentationMode'

/**
 * The presenter's own screen, in a second browser window.
 *
 * A meeting usually mirrors one display to the room and leaves the laptop for
 * the speaker, so the two need different content: the room gets the slide, the
 * speaker gets the clock, what is coming next, and the full text that the slide
 * had to shorten to stay readable from across the room.
 *
 * It renders through a portal into a window this component opens, so it is the
 * same React tree and stays in step with the deck without any message passing.
 */

/**
 * The presenter window follows the deck's theme rather than keeping a palette
 * of its own. A speaker who chose the light theme because the room is bright is
 * usually sitting in that same bright room.
 */
const presenterStyles = `
  :root { color-scheme: dark; --bg:#0b1120; --fg:#e2e8f0; --panel:#111c34; --line:#1e2f52;
          --dim:#7f93ba; --body:#cbd5e1; --accent:#60a5fa; --strong:#e2e8f0 }
  :root.theme-light { color-scheme: light; --bg:#f6f8fc; --fg:#0f172a; --panel:#fff; --line:#cbd5e1;
          --dim:#64748b; --body:#334155; --accent:#1d4ed8; --strong:#0f172a }
  :root.theme-contrast { color-scheme: dark; --bg:#000; --fg:#fff; --panel:#111; --line:#ffffff66;
          --dim:#cbd5e1; --body:#fff; --accent:#ffe066; --strong:#fff }
  body { margin:0; background:var(--bg); color:var(--fg);
    font-family:Inter,"Pretendard","Noto Sans KR",system-ui,sans-serif }
  .wrap { display:grid; grid-template-rows:auto 1fr auto; height:100vh; box-sizing:border-box; padding:20px 24px; gap:16px }
  header { display:flex; align-items:baseline; justify-content:space-between; gap:16px; flex-wrap:wrap }
  .clock { font-size:44px; font-weight:800; font-variant-numeric:tabular-nums; letter-spacing:.02em }
  .clock small { display:block; font-size:12px; font-weight:600; color:var(--dim); letter-spacing:.1em }
  .count { font-size:15px; color:var(--dim) }
  .grid { display:grid; grid-template-columns:1.6fr 1fr; gap:18px; min-height:0 }
  section { background:var(--panel); border:1px solid var(--line); border-radius:14px; padding:16px 18px; min-height:0; overflow:auto }
  h2 { margin:0 0 4px; font-size:22px; line-height:1.3 }
  .eyebrow { display:block; font-size:11px; font-weight:700; letter-spacing:.12em; color:var(--accent); margin-bottom:6px }
  .label { font-size:11px; font-weight:700; letter-spacing:.1em; color:var(--dim); margin:0 0 8px }
  pre { margin:0; white-space:pre-wrap; word-break:break-word; font:inherit; font-size:15px; line-height:1.65; color:var(--body) }
  .next h3 { margin:0; font-size:16px; line-height:1.4; color:var(--strong) }
  .next p { margin:6px 0 0; font-size:13px; color:var(--dim) }
  ul { margin:8px 0 0; padding-left:18px; font-size:13px; line-height:1.6; color:var(--body) }
  footer { font-size:12px; color:var(--dim); display:flex; gap:14px; flex-wrap:wrap }
  kbd { background:var(--line); border-radius:5px; padding:2px 6px; font-size:11px; color:var(--body) }
  .empty { color:var(--dim); font-size:14px }
`

function setDocumentTheme(target: Document, theme: string) {
  target.documentElement.className = `theme-${theme}`
}

function clockText(seconds: number) {
  const minutes = Math.floor(seconds / 60)
  return `${String(minutes).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
}

export default function PresenterWindow({ slides, position, label, notes, theme, elapsed, onClose, onKey }: {
  slides: PresentSlide[]
  position: number
  label: string
  notes?: string[]
  theme: string
  elapsed: number
  onClose: () => void
  onKey: (event: KeyboardEvent) => void
}) {
  const [host, setHost] = useState<HTMLElement | null>(null)
  // The window is opened once, so its listeners must read the current handlers
  // rather than the ones that existed at that moment. Without this, keys that
  // depend on what is on screen — Escape, B, O — would act on state from the
  // instant the presenter view was opened.
  const latest = useRef({ onKey, onClose })
  latest.current = { onKey, onClose }

  useEffect(() => {
    const opened = window.open('', 'weekly-presenter', 'width=1100,height=760')
    if (!opened) {
      // Pop-up blocked. Falling back to the deck alone is the only thing left;
      // silently doing nothing would look like the button is broken.
      latest.current.onClose()
      return
    }
    opened.document.title = `${label} · 발표자 화면`
    const style = opened.document.createElement('style')
    style.textContent = presenterStyles
    opened.document.head.appendChild(style)
    setDocumentTheme(opened.document, theme)
    const mount = opened.document.createElement('div')
    opened.document.body.appendChild(mount)
    setHost(mount)

    // The speaker's hands are usually on this window, so it drives the deck too.
    const onWindowKey = (event: KeyboardEvent) => latest.current.onKey(event)
    const onWindowClose = () => latest.current.onClose()
    opened.addEventListener('keydown', onWindowKey)
    opened.addEventListener('pagehide', onWindowClose)
    // Closing the deck must close this window; leaving an orphan behind on a
    // shared meeting laptop is worse than not opening it.
    const watch = window.setInterval(() => { if (opened.closed) latest.current.onClose() }, 500)
    return () => {
      window.clearInterval(watch)
      opened.removeEventListener('keydown', onWindowKey)
      opened.removeEventListener('pagehide', onWindowClose)
      if (!opened.closed) opened.close()
    }
    // Opened once for the lifetime of the presenter view on purpose.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (host?.ownerDocument) setDocumentTheme(host.ownerDocument, theme)
  }, [host, theme])

  if (!host) return null
  const slide = slides[position]
  const next = slides[position + 1]

  return createPortal(
    <div className="wrap">
      <header>
        <div className="clock">{clockText(elapsed)}<small>경과 시간</small></div>
        <div className="count">{position + 1} / {slides.length} · {label}</div>
      </header>
      <div className="grid">
        <section>
          {slide?.eyebrow && <span className="eyebrow">{slide.eyebrow}</span>}
          <h2>{slide?.title}</h2>
          {slide?.subtitle && <p className="next p">{slide.subtitle}</p>}
          <p className="label">전체 내용</p>
          {slide?.presenterText
            ? <pre>{slide.presenterText}</pre>
            : <p className="empty">이 슬라이드에는 추가로 읽을 내용이 없습니다.</p>}
          {slide?.note && <><p className="label">판단 근거</p><pre>{slide.note}</pre></>}
        </section>
        <section className="next">
          <p className="label">다음 슬라이드</p>
          {next
            ? <><h3>{next.title}</h3>{next.eyebrow && <p>{next.eyebrow}</p>}</>
            : <p className="empty">마지막 슬라이드입니다.</p>}
          {notes && notes.length > 0 && <>
            <p className="label" style={{ marginTop: 18 }}>진행 안내</p>
            <ul>{notes.map(note => <li key={note}>{note}</li>)}</ul>
          </>}
        </section>
      </div>
      <footer>
        <span><kbd>←</kbd> <kbd>→</kbd> 이동</span>
        <span><kbd>O</kbd> 슬라이드 목록</span>
        <span><kbd>B</kbd> 화면 끄기</span>
        <span><kbd>T</kbd> 시간 초기화</span>
        <span><kbd>P</kbd> 이 창 닫기</span>
      </footer>
    </div>, host)
}
