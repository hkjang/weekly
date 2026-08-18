import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

/**
 * Presentation mode: any deck the app can export as PPTX can also be presented
 * directly, full screen, from the keyboard.
 *
 * One item per slide rather than a scrolling list. A room reads the slide that
 * is up; anything else on screen competes with the speaker.
 *
 * Keys follow what presentation software has trained everyone to expect:
 * arrows, space and page keys move, Home and End jump to the ends, F toggles
 * full screen, Escape leaves. Clicking the slide also advances, because a
 * laptop on a meeting table is often operated by whoever is nearest.
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
}

export default function PresentationMode({ slides, label, notes, onClose }: {
  slides: PresentSlide[]
  /** Shown in the control bar so the presenter knows which deck is up. */
  label: string
  /** Optional facilitation notes, toggled with N. */
  notes?: string[]
  onClose: () => void
}) {
  const [position, setPosition] = useState(0)
  const [notesOpen, setNotesOpen] = useState(false)
  const container = useRef<HTMLDivElement>(null)

  const total = slides.length
  const go = useCallback((delta: number) => {
    setPosition(current => Math.min(Math.max(current + delta, 0), total - 1))
  }, [total])

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
    onClose()
  }, [onClose])

  useEffect(() => {
    container.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      switch (event.key) {
        case 'ArrowRight': case 'ArrowDown': case 'PageDown': case ' ': case 'Enter':
          event.preventDefault(); go(1); break
        case 'ArrowLeft': case 'ArrowUp': case 'PageUp': case 'Backspace':
          event.preventDefault(); go(-1); break
        case 'Home': event.preventDefault(); setPosition(0); break
        case 'End': event.preventDefault(); setPosition(total - 1); break
        case 'f': case 'F': event.preventDefault(); toggleFullscreen(); break
        case 'n': case 'N':
          if (notes?.length) { event.preventDefault(); setNotesOpen(open => !open) }
          break
        case 'Escape': event.preventDefault(); leave(); break
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [go, leave, notes, total, toggleFullscreen])

  const slide = useMemo(() => slides[Math.min(position, total - 1)], [slides, position, total])
  const progress = total > 1 ? (position / (total - 1)) * 100 : 100

  if (!slide) return null

  return <div className="presentation" ref={container} tabIndex={-1} role="dialog" aria-label={`${label} 발표 모드`}
    onClick={event => { if (event.target === event.currentTarget) go(1) }}>
    <div className="presentation-bar"><span style={{ width: `${progress}%` }} /></div>

    <div className={`presentation-slide ${slide.tone ? `tone-${slide.tone.toLowerCase()}` : ''} kind-${slide.kind}`}>
      <div>
        {slide.eyebrow && <span className="slide-eyebrow">{slide.eyebrow}</span>}
        <h1>{slide.title}</h1>
        {slide.subtitle && <p className="slide-subtitle">{slide.subtitle}</p>}
        {slide.meta && slide.meta.length > 0 && <div className="slide-meta">
          {slide.meta.map(fact => <span key={fact}>{fact}</span>)}
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

    {notesOpen && notes && notes.length > 0 && <div className="presentation-notes">
      <strong>진행 안내</strong>
      <ul>{notes.map(note => <li key={note}>{note}</li>)}</ul>
    </div>}

    <div className="presentation-controls" onClick={event => event.stopPropagation()}>
      <span className="presentation-label">{label}</span>
      <button onClick={() => go(-1)} disabled={position === 0} aria-label="이전 슬라이드">◀</button>
      <span>{position + 1} / {total}</span>
      <button onClick={() => go(1)} disabled={position === total - 1} aria-label="다음 슬라이드">▶</button>
      <button onClick={toggleFullscreen}>전체화면</button>
      {notes && notes.length > 0 && <button onClick={() => setNotesOpen(open => !open)}>진행 안내</button>}
      <button className="secondary" onClick={leave}>종료</button>
    </div>
  </div>
}
