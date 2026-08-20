import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import PresenterWindow from './PresenterWindow'
import { api } from './api'
import { isTopLayer, popLayer, pushLayer } from './layers'

/**
 * Presentation mode: any deck the app can export as PPTX can also be presented
 * directly, full screen, from the keyboard.
 *
 * One item per slide rather than a scrolling list. A room reads the slide that
 * is up; anything else on screen competes with the speaker.
 *
 * Keys follow what presentation software has trained everyone to expect:
 * arrows, space and page keys move, Home and End jump to the ends, F toggles
 * full screen, Escape leaves. O opens the slide list, a typed number jumps, B
 * blanks the screen, P opens the presenter window, T resets the clock. Clicking
 * the slide also advances, because a laptop on a meeting table is often
 * operated by whoever is nearest.
 *
 * The deck itself is built by the caller (see presentSlides.ts) so this file
 * stays a presenter and knows nothing about reports, rollups or agendas.
 */

export interface SlideBlock {
  label: string
  text: string
  tone?: 'issue' | 'ask' | 'plan'
}

export interface SlideImage {
  url: string
  /** Shown under the image and used as the alt text. */
  caption: string
  width: number
  height: number
}

export interface PresentSlide {
  kind: 'cover' | 'section' | 'entry' | 'end' | 'image'
  /** Small label above the title: which section this is, and where in it. */
  eyebrow?: string
  title: string
  subtitle?: string
  /** Short facts shown as chips: owner, organization, progress. */
  meta?: string[]
  body?: string
  blocks?: SlideBlock[]
  /** A single highlighted line — the reason this slide is in the deck. */
  note?: string
  /** Colour key, used for the eyebrow accent. */
  tone?: string
  /** A capture slide: the image is the slide, so it gets the whole frame. */
  image?: SlideImage
  /**
   * The same content with nothing cut, shown only on the presenter's screen.
   * The slide is shortened to stay readable across a room; the speaker is the
   * one person who needs every word.
   */
  presenterText?: string
}

/**
 * The smallest type a room can still read, as a share of the screen height.
 *
 * Projection guidance converges on roughly one fiftieth of the image height, so
 * the floor moves with the screen instead of being a number picked against one
 * laptop. Shrinking past it would trade a visible problem for an invisible one:
 * the slide would fit and nobody past the second row could read it.
 */
const READABLE_SHARE = 1 / 50
const READABLE_FLOOR_PX = 14

/** Never scale below this regardless, so one enormous slide cannot vanish. */
const HARD_FIT_FLOOR = 0.5

export type PresentTheme = 'dark' | 'light' | 'contrast'

const themeOrder: PresentTheme[] = ['dark', 'light', 'contrast']
const themeName: Record<PresentTheme, string> = {
  dark: '어두운 화면', light: '밝은 화면', contrast: '고대비',
}

export default function PresentationMode({ slides, label, notes, onClose }: {
  slides: PresentSlide[]
  /** Shown in the control bar so the presenter knows which deck is up. */
  label: string
  /** Optional facilitation notes, toggled with N. */
  notes?: string[]
  onClose: () => void
}) {
  const total = slides.length
  // Restored across a reload: losing your place in a forty slide deck in front
  // of a room is a small disaster, and the position is cheap to remember.
  const storageKey = `weekly.present.${label}`
  const [position, setPosition] = useState(() => {
    const saved = Number(window.sessionStorage.getItem(storageKey))
    return Number.isInteger(saved) && saved > 0 && saved < total ? saved : 0
  })
  const [notesOpen, setNotesOpen] = useState(false)
  const [overviewOpen, setOverviewOpen] = useState(false)
  const [blackout, setBlackout] = useState(false)
  const [presenterOpen, setPresenterOpen] = useState(false)
  const [elapsed, setElapsed] = useState(0)
  // One measurement, from which both the scale and the warning are derived at
  // render time. Keeping them as separate pieces of state let one of them go
  // stale when a measurement was skipped.
  const [measured, setMeasured] = useState({ available: 0, needed: 0, edge: 0, floor: HARD_FIT_FLOOR, compact: false })
  // A preference rather than a per-meeting choice: the room a team presents in
  // does not change every week, so it is remembered across sessions.
  const [theme, setTheme] = useState<PresentTheme>(() => {
    const saved = window.localStorage.getItem('weekly.present.theme')
    return themeOrder.includes(saved as PresentTheme) ? saved as PresentTheme : 'dark'
  })
  // The accent the organisation's own PPTX template uses, so the screen and the
  // exported file are recognisably the same deck.
  const [accent, setAccent] = useState<string>()
  const container = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)
  const fitted = useRef<HTMLDivElement>(null)

  const go = useCallback((delta: number) => {
    setPosition(current => Math.min(Math.max(current + delta, 0), total - 1))
  }, [total])
  const jump = useCallback((index: number) => {
    setPosition(Math.min(Math.max(index, 0), total - 1))
    setOverviewOpen(false)
  }, [total])

  useEffect(() => { window.sessionStorage.setItem(storageKey, String(position)) }, [storageKey, position])

  // Full screen must be requested from a user gesture, which is the only
  // context a browser allows, and failure is not worth interrupting a meeting.
  const toggleFullscreen = useCallback(() => {
    const element = container.current
    if (!element) return
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => undefined)
    else void element.requestFullscreen?.().catch(() => undefined)
  }, [])

  const leave = useCallback(() => {
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => undefined)
    window.sessionStorage.removeItem(storageKey)
    onClose()
  }, [onClose, storageKey])

  // A typed number jumps straight to that slide, the way every remote-free
  // presenter has learned to do it. The digits are collected for a moment
  // rather than acting on each keystroke, so "12" does not stop at slide 1.
  const typed = useRef({ digits: '', timer: 0 })
  const pushDigit = useCallback((digit: string) => {
    window.clearTimeout(typed.current.timer)
    typed.current.digits += digit
    typed.current.timer = window.setTimeout(() => {
      const target = Number(typed.current.digits)
      typed.current.digits = ''
      if (target >= 1) jump(target - 1)
    }, 700)
  }, [jump])

  const onKey = useCallback((event: KeyboardEvent) => {
    if (event.ctrlKey || event.metaKey || event.altKey) return
    if (event.key >= '0' && event.key <= '9') { event.preventDefault(); pushDigit(event.key); return }
    switch (event.key) {
      case 'ArrowRight': case 'ArrowDown': case 'PageDown': case ' ': case 'Enter':
        event.preventDefault(); go(1); break
      case 'ArrowLeft': case 'ArrowUp': case 'PageUp': case 'Backspace':
        event.preventDefault(); go(-1); break
      case 'Home': event.preventDefault(); setPosition(0); break
      case 'End': event.preventDefault(); setPosition(total - 1); break
      case 'f': case 'F': event.preventDefault(); toggleFullscreen(); break
      case 'o': case 'O': event.preventDefault(); setOverviewOpen(open => !open); break
      case 'b': case 'B': event.preventDefault(); setBlackout(dark => !dark); break
      case 'p': case 'P': event.preventDefault(); setPresenterOpen(open => !open); break
      case 't': case 'T': event.preventDefault(); setElapsed(0); break
      case 'c': case 'C':
        event.preventDefault()
        setTheme(current => themeOrder[(themeOrder.indexOf(current) + 1) % themeOrder.length])
        break
      case 'n': case 'N':
        if (notes?.length) { event.preventDefault(); setNotesOpen(open => !open) }
        break
      case 'Escape':
        event.preventDefault()
        // One step back at a time: an overlay closes before the deck does, so
        // Escape never ends a meeting by surprise.
        if (overviewOpen) setOverviewOpen(false)
        else if (blackout) setBlackout(false)
        else if (notesOpen) setNotesOpen(false)
        else leave()
        break
    }
  }, [blackout, go, jump, leave, notes, notesOpen, overviewOpen, pushDigit, toggleFullscreen, total])

  // Registered in the shared layer stack: a deck opened over a detail dialog
  // must be the only thing Escape closes.
  const layer = useRef<symbol>(undefined)
  useEffect(() => {
    layer.current = pushLayer()
    return () => { if (layer.current) popLayer(layer.current) }
  }, [])
  useEffect(() => {
    container.current?.focus()
    const guarded = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && layer.current && !isTopLayer(layer.current)) return
      onKey(event)
    }
    window.addEventListener('keydown', guarded)
    return () => window.removeEventListener('keydown', guarded)
  }, [onKey])

  useEffect(() => {
    const tick = window.setInterval(() => setElapsed(seconds => seconds + 1), 1000)
    return () => window.clearInterval(tick)
  }, [])

  const slide = useMemo(() => slides[Math.min(position, total - 1)], [slides, position, total])

  // Make the slide fit instead of letting overflow:hidden cut it off, and do it
  // in the order a designer would: take the whitespace first, and only then the
  // type. Shrinking type is what costs the room its ability to read the slide,
  // so it is the last resort rather than the first move.
  useLayoutEffect(() => {
    const box = content.current
    if (!box) return
    setMeasured({ available: 0, needed: 0, edge: 0, floor: HARD_FIT_FLOOR, compact: false })
    const measure = () => {
      const element = content.current
      const inner = fitted.current
      if (!element || !inner) return
      const available = element.clientHeight
      // A frame where the slide has not been laid out yet reports no height at
      // all. Scaling to that would shrink a slide that fits perfectly well.
      if (available <= 1) return
      // Measured on the content itself, not on the box around it. A scaled
      // descendant contributes its *transformed* size to an ancestor's scroll
      // height, so measuring the ancestor would feed the previous scale back
      // into the next one and settle on the wrong answer.
      const heightOf = () => Math.max(inner.offsetHeight, inner.scrollHeight)
      inner.classList.remove('compact')
      let needed = heightOf()
      let compact = false
      if (needed > available) {
        // Tightening the gaps often buys the whole difference on its own, and
        // costs nothing anyone can read.
        inner.classList.add('compact')
        compact = true
        needed = heightOf()
      }
      if (needed <= 0) return
      // The floor is whatever keeps the smallest text on the slide readable
      // from the back of the room, not a fixed ratio.
      const smallest = smallestTypeIn(inner)
      const readable = Math.max(READABLE_FLOOR_PX, (element.parentElement?.clientHeight ?? available) * READABLE_SHARE)
      const floor = smallest > 0 ? Math.min(1, Math.max(readable / smallest, HARD_FIT_FLOOR)) : HARD_FIT_FLOOR
      setMeasured({ available, needed, edge: element.parentElement?.clientHeight ?? available, floor, compact })
    }
    // Measured after the browser has laid the slide out at full size, and again
    // whenever that layout changes: entering full screen, rotating a tablet and
    // a late web font all move the goalposts after the first frame.
    const frame = window.requestAnimationFrame(measure)
    const observer = new ResizeObserver(measure)
    observer.observe(box)
    return () => { window.cancelAnimationFrame(frame); observer.disconnect() }
  }, [slide])

  const fit = measured.available > 0 && measured.needed > measured.available
    ? Math.max(measured.available / measured.needed, measured.floor)
    : 1
  // Anything taller than the box it is anchored in is being cut, because the
  // slide hides its overflow. The padding is not spare room: content that runs
  // into it ends up flush against the control bar and looks broken long before
  // it disappears.
  const clipped = measured.needed > 0 && measured.needed * fit > measured.available + 2

  useEffect(() => { window.localStorage.setItem('weekly.present.theme', theme) }, [theme])

  // Asked for once per deck. A failure just leaves the built-in accent in place,
  // which is what a deployment without a corporate template gets anyway.
  useEffect(() => {
    let stale = false
    api<{ accent?: string }>('/api/v1/present/theme')
      .then(value => { if (!stale && value?.accent) setAccent(value.accent) })
      .catch(() => undefined)
    return () => { stale = true }
  }, [])

  // The next capture is fetched while the current slide is up, so an image
  // slide does not open on an empty frame.
  useEffect(() => {
    const upcoming = slides[position + 1]?.image?.url
    if (!upcoming) return
    const image = new Image()
    image.src = upcoming
  }, [slides, position])

  // A tablet on a meeting table has no keyboard. Tapping already advances;
  // swiping is the only way back.
  const touch = useRef(0)
  const onTouchStart = (event: React.TouchEvent) => { touch.current = event.changedTouches[0].clientX }
  const onTouchEnd = (event: React.TouchEvent) => {
    const travelled = event.changedTouches[0].clientX - touch.current
    if (Math.abs(travelled) > 60) go(travelled < 0 ? 1 : -1)
  }

  if (!slide) return null
  const progress = total > 1 ? (position / (total - 1)) * 100 : 100

  // The accent only overrides the deck-wide default. A section that has a
  // colour of its own keeps it, because that colour carries meaning the brand
  // colour does not.
  const surface = accent ? { '--pres-accent': accent, '--pres-accent-strong': accent } as React.CSSProperties : undefined

  return <div className={`presentation theme-${theme}`} style={surface} ref={container} tabIndex={-1} role="dialog" aria-label={`${label} 발표 모드`}
    onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}
    onClick={event => { if (event.target === event.currentTarget) go(1) }}>
    <div className="presentation-bar"><span style={{ width: `${progress}%` }} /></div>

    {/* Announced for screen readers; the slide itself carries no live region. */}
    <p className="visually-hidden" aria-live="polite">{`${position + 1} / ${total} · ${slide.title}`}</p>

    <div className={`presentation-slide ${slide.tone ? `tone-${slide.tone.toLowerCase()}` : ''} kind-${slide.kind}${clipped ? ' is-clipped' : ''}`}>
      <div ref={content}>
        {/* Anchored at the top: scaling around the centre would pull the block
            downward, because the unscaled content overflows this box downward
            rather than in both directions. */}
        <div className={`slide-fit${measured.compact ? ' compact' : ''}`} ref={fitted} style={{ transform: `scale(${fit})` }}>
          {slide.eyebrow && <span className="slide-eyebrow">{slide.eyebrow}</span>}
          <h1>{slide.title}</h1>
          {slide.subtitle && <p className="slide-subtitle">{slide.subtitle}</p>}
          {slide.meta && slide.meta.length > 0 && <div className="slide-meta">
            {slide.meta.map((fact, index) => <span key={`${fact}-${index}`}>{fact}</span>)}
          </div>}
          {slide.image && <figure className="slide-image">
            {/* Contained rather than cropped: a screen capture loses its point
                when its edges are cut off. */}
            <img src={slide.image.url} alt={slide.image.caption} width={slide.image.width} height={slide.image.height} />
            {slide.image.caption && <figcaption>{slide.image.caption}</figcaption>}
          </figure>}
          {slide.body && <p className="slide-body">{slide.body}</p>}
          {slide.blocks && slide.blocks.length > 0 && <div className="slide-blocks">
            {slide.blocks.map(block => <section key={block.label} className={block.tone ? `tone-${block.tone}` : undefined}>
              <b>{block.label}</b>
              <p>{block.text}</p>
            </section>)}
          </div>}
          {slide.note && <p className="slide-note">{slide.note}</p>}
        </div>
      </div>
    </div>

    {clipped && <div className="slide-clipped">이 슬라이드는 화면에 다 들어가지 않습니다. 전체 내용은 발표자 화면(P)이나 보고서에서 확인하세요.</div>}

    {blackout && <div className="presentation-blackout" onClick={event => { event.stopPropagation(); setBlackout(false) }}>
      <span>화면을 껐습니다 · B 또는 클릭으로 복귀</span>
    </div>}

    {overviewOpen && <div className="presentation-overview" onClick={event => event.stopPropagation()}>
      <header><strong>슬라이드 {total}장</strong><span>번호를 입력하거나 눌러서 이동합니다.</span>
        <button onClick={() => setOverviewOpen(false)} aria-label="목록 닫기">×</button></header>
      <ol>{slides.map((item, index) => <li key={index}>
        <button className={index === position ? 'active' : ''} onClick={() => jump(index)}>
          <b>{index + 1}</b>
          <span>
            {item.eyebrow && <small>{item.eyebrow}</small>}
            {item.title}
          </span>
        </button>
      </li>)}</ol>
    </div>}

    {notesOpen && notes && notes.length > 0 && <div className="presentation-notes">
      <strong>진행 안내</strong>
      <ul>{notes.map(note => <li key={note}>{note}</li>)}</ul>
    </div>}

    <div className="presentation-controls" onClick={event => event.stopPropagation()}>
      <span className="presentation-label">{label}</span>
      <button onClick={() => go(-1)} disabled={position === 0} aria-label="이전 슬라이드">◀</button>
      <span>{position + 1} / {total}</span>
      <button onClick={() => go(1)} disabled={position === total - 1} aria-label="다음 슬라이드">▶</button>
      <button onClick={() => setOverviewOpen(open => !open)}>목록</button>
      <button className={presenterOpen ? 'on' : ''} onClick={() => setPresenterOpen(open => !open)}>발표자 화면</button>
      <button onClick={() => setTheme(current => themeOrder[(themeOrder.indexOf(current) + 1) % themeOrder.length])}
        title="C 키로도 바꿉니다">{themeName[theme]}</button>
      <button onClick={toggleFullscreen}>전체화면</button>
      {notes && notes.length > 0 && <button onClick={() => setNotesOpen(open => !open)}>진행 안내</button>}
      <button className="secondary" onClick={leave}>종료</button>
    </div>

    {presenterOpen && <PresenterWindow slides={slides} position={position} label={label} notes={notes}
      theme={theme} elapsed={elapsed} onClose={() => setPresenterOpen(false)} onKey={onKey} />}
  </div>
}

/**
 * The smallest font size actually used on a slide.
 *
 * Read from the rendered slide rather than from a constant, because the labels
 * that end up smallest differ by slide kind, and a constant would go stale the
 * first time the stylesheet changed.
 */
function smallestTypeIn(root: HTMLElement): number {
  let smallest = Number.POSITIVE_INFINITY
  for (const element of root.querySelectorAll<HTMLElement>('*')) {
    // Only elements that print text of their own. A wrapper reports all of its
    // descendants' text as its own, and it usually sits at the inherited 16px,
    // so counting wrappers would peg the floor to a size nothing is drawn at.
    const writes = Array.from(element.childNodes).some(
      node => node.nodeType === Node.TEXT_NODE && node.textContent?.trim())
    if (!writes) continue
    const size = parseFloat(window.getComputedStyle(element).fontSize)
    if (size > 0 && size < smallest) smallest = size
  }
  return Number.isFinite(smallest) ? smallest : 0
}
