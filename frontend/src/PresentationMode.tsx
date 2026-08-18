import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { MeetingEntry, MeetingView } from './types'

/**
 * Presentation mode: the meeting agenda as full screen slides, driven from the
 * keyboard so the person talking never has to look for a control.
 *
 * One agenda item per slide rather than a scrolling list. A room reads the
 * slide that is up; anything else on screen is competing with the speaker.
 *
 * Keys follow what presentation software has trained everyone to expect:
 * arrows, space and page keys move, Home and End jump to the ends, F toggles
 * full screen, Escape leaves. It is also driven by clicking, because a laptop
 * on a meeting table is often operated by whoever is nearest.
 */

interface Slide {
  kind: 'cover' | 'section' | 'entry' | 'end'
  sectionTitle?: string
  sectionPurpose?: string
  sectionKey?: string
  index?: number
  count?: number
  entry?: MeetingEntry
}

function buildSlides(view: MeetingView): Slide[] {
  const slides: Slide[] = [{ kind: 'cover' }]
  for (const section of view.sections) {
    if (!section.entries.length) continue
    slides.push({ kind: 'section', sectionTitle: section.title, sectionPurpose: section.purpose, sectionKey: section.key, count: section.entries.length })
    section.entries.forEach((entry, index) => slides.push({
      kind: 'entry', sectionTitle: section.title, sectionKey: section.key,
      entry, index: index + 1, count: section.entries.length,
    }))
  }
  slides.push({ kind: 'end' })
  return slides
}

export default function PresentationMode({ view, onClose }: { view: MeetingView; onClose: () => void }) {
  const slides = useMemo(() => buildSlides(view), [view])
  const [position, setPosition] = useState(0)
  const [notesOpen, setNotesOpen] = useState(false)
  const container = useRef<HTMLDivElement>(null)

  const go = useCallback((delta: number) => {
    setPosition(current => Math.min(Math.max(current + delta, 0), slides.length - 1))
  }, [slides.length])

  // Full screen is requested from a user gesture, which is the only context a
  // browser allows it in, and failure is not worth interrupting a meeting over.
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
        case 'End': event.preventDefault(); setPosition(slides.length - 1); break
        case 'f': case 'F': event.preventDefault(); toggleFullscreen(); break
        case 'n': case 'N': event.preventDefault(); setNotesOpen(open => !open); break
        case 'Escape': event.preventDefault(); leave(); break
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [go, leave, slides.length, toggleFullscreen])

  const slide = slides[position]
  const progress = slides.length > 1 ? (position / (slides.length - 1)) * 100 : 100

  return <div className="presentation" ref={container} tabIndex={-1} role="dialog" aria-label="회의 발표 모드"
    onClick={event => { if (event.target === event.currentTarget) go(1) }}>
    <div className="presentation-bar"><span style={{ width: `${progress}%` }} /></div>

    <div className={`presentation-slide ${slide.sectionKey ? `tone-${slide.sectionKey.toLowerCase()}` : ''}`}>
      {slide.kind === 'cover' && <div className="slide-cover">
        <span className="slide-eyebrow">{view.scope === 'TEAM' ? '조직 주간 회의' : '개인 주간 점검'}</span>
        <h1>{view.week} 주차 회의</h1>
        <p>업무 {view.workItems}건 · 담당자 {view.people}명 · 안건 {slides.length - 2}건</p>
        <p className="slide-hint">→ 또는 스페이스로 진행 · F 전체화면 · N 진행 안내 · Esc 종료</p>
      </div>}

      {slide.kind === 'section' && <div className="slide-section">
        <span className="slide-eyebrow">{slide.count}건</span>
        <h1>{slide.sectionTitle}</h1>
        <p>{slide.sectionPurpose}</p>
      </div>}

      {slide.kind === 'entry' && slide.entry && <div className="slide-entry">
        <span className="slide-eyebrow">{slide.sectionTitle} {slide.index}/{slide.count}</span>
        <h1>{slide.entry.title}</h1>
        <div className="slide-meta">
          <span>{slide.entry.displayName}</span>
          {slide.entry.organizationName && <span>{slide.entry.organizationName}</span>}
          {slide.entry.category && <span>{slide.entry.category}</span>}
          <span>진척 {slide.entry.progress}%</span>
          {slide.entry.progressDelta !== 0 && <span className={slide.entry.progressDelta > 0 ? 'delta-up' : 'delta-down'}>
            {slide.entry.progressDelta > 0 ? `+${slide.entry.progressDelta}` : slide.entry.progressDelta}%</span>}
          <span>{slide.entry.weeks}주 보고</span>
        </div>
        {slide.entry.detail && <p className="slide-body">{slide.entry.detail}</p>}
        {slide.entry.note && <p className="slide-note">{slide.entry.note}</p>}
      </div>}

      {slide.kind === 'end' && <div className="slide-cover">
        <h1>안건 종료</h1>
        <p>결정 사항과 후속 조치는 각 담당자의 다음 주 보고에 반영합니다.</p>
      </div>}
    </div>

    {notesOpen && <div className="presentation-notes">
      <strong>진행 안내</strong>
      <ul>
        <li>결정 필요 항목은 이 자리에서 결론을 남기고 넘어갑니다.</li>
        <li>지속 이슈는 상태 확인이 아니라 접근 방식을 바꿀지 정합니다.</li>
        <li>보고 누락은 담당자에게 현황을 직접 확인합니다.</li>
      </ul>
    </div>}

    <div className="presentation-controls" onClick={event => event.stopPropagation()}>
      <button onClick={() => go(-1)} disabled={position === 0} aria-label="이전 슬라이드">◀</button>
      <span>{position + 1} / {slides.length}</span>
      <button onClick={() => go(1)} disabled={position === slides.length - 1} aria-label="다음 슬라이드">▶</button>
      <button onClick={toggleFullscreen}>전체화면</button>
      <button onClick={() => setNotesOpen(open => !open)}>진행 안내</button>
      <button className="secondary" onClick={leave}>종료</button>
    </div>
  </div>
}
