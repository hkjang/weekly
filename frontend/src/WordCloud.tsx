import { useMemo } from 'react'
import type { AnalysisTerm } from './types'

/**
 * Word cloud with spiral placement and collision detection.
 *
 * A cloud is a weak encoding: font size is hard to compare precisely, so this
 * is the overview and the ranked table beside it carries the exact numbers.
 * Colour is not decorative here — it encodes how the term moved against the
 * previous period, which is the thing a reader cannot get from size alone.
 */

const cloudWidth = 760
const cloudHeight = 380

export type TermTrend = 'new' | 'up' | 'flat' | 'down'

export const trendColors: Record<TermTrend, string> = {
  new: '#0d9488',
  up: '#2563eb',
  flat: '#64748b',
  down: '#94a3b8',
}

export function termTrend(term: AnalysisTerm): TermTrend {
  if (term.delta >= term.count) return 'new'
  if (term.delta > 0) return 'up'
  if (term.delta < 0) return 'down'
  return 'flat'
}

interface Placed { term: AnalysisTerm; x: number; y: number; size: number; color: string }

/** Korean glyphs are about full width, latin about half, which is close enough
 *  to reserve space without measuring the real font. */
function estimateWidth(text: string, size: number) {
  let units = 0
  for (const character of text) {
    units += /[가-힣㄰-㆏]/.test(character) ? 1 : /\s/.test(character) ? 0.35 : 0.56
  }
  return units * size
}

function layout(terms: AnalysisTerm[], showTrend: boolean): Placed[] {
  if (!terms.length) return []
  const maximum = terms[0].weight || 1
  const minimum = terms[terms.length - 1].weight || 0
  const span = Math.max(0.0001, maximum - minimum)
  const placed: Placed[] = []
  const boxes: { x: number; y: number; w: number; h: number }[] = []

  for (const term of terms) {
    // Square-root scaling keeps the largest term from swallowing the canvas.
    const ratio = Math.sqrt((term.weight - minimum) / span)
    const size = Math.round(13 + ratio * 33)
    const width = estimateWidth(term.term, size)
    const height = size * 1.15
    let position: { x: number; y: number } | undefined
    // Archimedean spiral outward from the centre until a free slot is found.
    for (let step = 0; step < 2600; step++) {
      const angle = step * 0.32
      const radius = 3 + angle * 3.1
      const x = cloudWidth / 2 + radius * Math.cos(angle) * 1.55 - width / 2
      const y = cloudHeight / 2 + radius * Math.sin(angle) * 0.72 - height / 2
      if (x < 4 || y < 4 || x + width > cloudWidth - 4 || y + height > cloudHeight - 4) continue
      const hit = boxes.some(box => x < box.x + box.w + 5 && x + width + 5 > box.x && y < box.y + box.h + 3 && y + height + 3 > box.y)
      if (hit) continue
      position = { x, y }
      break
    }
    if (!position) continue
    boxes.push({ x: position.x, y: position.y, w: width, h: height })
    placed.push({ term, x: position.x, y: position.y + height * 0.78, size, color: showTrend ? trendColors[termTrend(term)] : '#2563eb' })
  }
  return placed
}

export default function WordCloud({ terms, onSelect, showTrend = true }: {
  terms: AnalysisTerm[]
  onSelect?: (term: AnalysisTerm) => void
  /** Without a comparison period every term looks new, so the trend colouring
   *  is suppressed rather than shown as a uniform block of one colour. */
  showTrend?: boolean
}) {
  const placed = useMemo(() => layout(terms.slice(0, 90), showTrend), [terms, showTrend])
  if (!terms.length) return <p className="muted">분석할 텍스트가 없습니다.</p>
  return <div className="wordcloud">
    <svg viewBox={`0 0 ${cloudWidth} ${cloudHeight}`} width="100%" role="img"
      aria-label={`상위 키워드 ${placed.length}개. 정확한 수치는 옆의 표를 확인하세요.`}>
      {placed.map(item => <text key={item.term.term} x={item.x} y={item.y}
        fontSize={item.size} fill={item.color} fontWeight={item.size > 26 ? 800 : 600}
        style={onSelect ? { cursor: 'pointer' } : undefined}
        onClick={onSelect ? () => onSelect(item.term) : undefined}>
        {item.term.term}
        <title>{`${item.term.term} · ${item.term.count}회 · ${item.term.documents}개 보고서 · 이전 대비 ${item.term.delta >= 0 ? '+' : ''}${item.term.delta}`}</title>
      </text>)}
    </svg>
    {showTrend ? <ul className="chart-legend">
      <li><i style={{ background: trendColors.new }} />신규</li>
      <li><i style={{ background: trendColors.up }} />증가</li>
      <li><i style={{ background: trendColors.flat }} />유지</li>
      <li><i style={{ background: trendColors.down }} />감소</li>
    </ul> : <p className="muted">비교할 직전 기간의 보고서가 없어 증감 색상은 표시하지 않습니다. 크기는 가중치입니다.</p>}
    {placed.length < Math.min(terms.length, 90) && <p className="muted">
      공간에 맞춰 {placed.length}개만 배치했습니다. 전체 순위는 오른쪽 표에서 확인하세요.
    </p>}
  </div>
}
