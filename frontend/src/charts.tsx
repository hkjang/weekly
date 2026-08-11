import { useState } from 'react'
import type { ReactNode } from 'react'

/**
 * Inline SVG chart primitives. The app ships under a strict CSP with no external
 * script or style hosts, so every chart here is hand drawn with plain SVG and the
 * existing design tokens. No chart runs on a second y-axis: a chart shows one
 * measure on one scale, and a second measure gets its own chart beside it.
 */

// Categorical slots for part-to-whole identity, validated for colour-vision
// deficiency separation against a white surface in this fixed order.
export const seriesColors = ['#2563eb', '#ea580c', '#0d9488', '#6d28d9', '#db2777', '#d97706']

// Status colours are reserved for delivery state and never reused as a series.
export const stateColors = {
  completed: '#16a34a',
  progress: '#2563eb',
  notStarted: '#94a3b8',
  stalled: '#d97706',
  risk: '#dc2626',
}

const axisInk = '#94a3b8'
const gridInk = '#eef2f7'

interface TooltipState { x: number; y: number; content: ReactNode }

/** A single absolutely positioned tooltip shared by every mark in one chart. */
function useTooltip() {
  const [tooltip, setTooltip] = useState<TooltipState>()
  const bind = (content: ReactNode) => ({
    onMouseMove: (event: React.MouseEvent) => {
      const box = event.currentTarget.closest('.chart-frame')?.getBoundingClientRect()
      if (!box) return
      setTooltip({ x: event.clientX - box.left, y: event.clientY - box.top, content })
    },
    onMouseLeave: () => setTooltip(undefined),
  })
  const node = tooltip
    ? <div className="chart-tooltip" style={{ left: tooltip.x, top: tooltip.y }}>{tooltip.content}</div>
    : null
  return { bind, node }
}

export function ChartFrame({ children, height }: { children: ReactNode; height?: number }) {
  return <div className="chart-frame" style={height ? { minHeight: height } : undefined}>{children}</div>
}

export function Legend({ items }: { items: { label: string; color: string }[] }) {
  return <ul className="chart-legend">
    {items.map(item => <li key={item.label}><i style={{ background: item.color }} />{item.label}</li>)}
  </ul>
}

function labelStride(count: number, maximum = 13) {
  return Math.max(1, Math.ceil(count / maximum))
}

function shortWeek(value: string) { return value.slice(5).replace('-', '/') }

/**
 * Weekly delivery state as a stacked column chart. Segments are ordered
 * 완료 → 진행 → 미착수 and separated by a 2px surface gap rather than a stroke.
 */
export function WeeklyStateChart({ data }: {
  data: { weekStart: string; completed: number; progress: number; notStarted: number }[]
}) {
  const { bind, node } = useTooltip()
  const width = 720, height = 220, padLeft = 34, padRight = 8, padTop = 12, padBottom = 30
  const plotWidth = width - padLeft - padRight, plotHeight = height - padTop - padBottom
  const maximum = Math.max(1, ...data.map(point => point.completed + point.progress + point.notStarted))
  const step = plotWidth / Math.max(1, data.length)
  const barWidth = Math.max(4, Math.min(30, step - 8))
  const ticks = [0, Math.round(maximum / 2), maximum].filter((value, index, all) => all.indexOf(value) === index)
  const stride = labelStride(data.length)

  return <ChartFrame>
    <svg viewBox={`0 0 ${width} ${height}`} width="100%" role="img" aria-label="주차별 업무 상태 추이">
      {ticks.map(tick => {
        const y = padTop + plotHeight - (tick / maximum) * plotHeight
        return <g key={tick}>
          <line x1={padLeft} x2={width - padRight} y1={y} y2={y} stroke={gridInk} strokeWidth={1} />
          <text x={padLeft - 7} y={y + 4} textAnchor="end" fontSize={10} fill={axisInk}>{tick}</text>
        </g>
      })}
      {data.map((point, index) => {
        const x = padLeft + index * step + (step - barWidth) / 2
        const segments = [
          { key: 'completed', label: '완료', value: point.completed, color: stateColors.completed },
          { key: 'progress', label: '진행', value: point.progress, color: stateColors.progress },
          { key: 'notStarted', label: '미착수', value: point.notStarted, color: stateColors.notStarted },
        ]
        const total = segments.reduce((sum, segment) => sum + segment.value, 0)
        let cursor = padTop + plotHeight
        return <g key={point.weekStart} {...bind(
          <><strong>{point.weekStart}</strong>{segments.map(segment =>
            <span key={segment.key}>{segment.label} {segment.value}건</span>)}<span>합계 {total}건</span></>
        )}>
          <rect x={x - 3} y={padTop} width={barWidth + 6} height={plotHeight} fill="transparent" />
          {segments.map(segment => {
            if (segment.value <= 0) return null
            const barHeight = (segment.value / maximum) * plotHeight
            cursor -= barHeight
            // A 2px surface gap separates stacked segments instead of a stroke.
            return <rect key={segment.key} x={x} y={cursor} width={barWidth}
              height={Math.max(1, barHeight - 2)} rx={2} fill={segment.color} />
          })}
        </g>
      })}
      <line x1={padLeft} x2={width - padRight} y1={padTop + plotHeight} y2={padTop + plotHeight} stroke={axisInk} strokeWidth={1} />
      {data.map((point, index) => index % stride === 0
        ? <text key={point.weekStart} x={padLeft + index * step + step / 2} y={height - 10}
            textAnchor="middle" fontSize={10} fill={axisInk}>{shortWeek(point.weekStart)}</text>
        : null)}
    </svg>
    {node}
    <Legend items={[{ label: '완료', color: stateColors.completed }, { label: '진행', color: stateColors.progress }, { label: '미착수', color: stateColors.notStarted }]} />
  </ChartFrame>
}

/** Average progress over the period on its own 0-100 scale. */
export function ProgressTrendChart({ data }: { data: { weekStart: string; averageProgress: number; activeItems: number }[] }) {
  const { bind, node } = useTooltip()
  const width = 720, height = 200, padLeft = 34, padRight = 12, padTop = 14, padBottom = 30
  const plotWidth = width - padLeft - padRight, plotHeight = height - padTop - padBottom
  const reported = data.filter(point => point.activeItems > 0)
  const step = data.length > 1 ? plotWidth / (data.length - 1) : 0
  const pointAt = (index: number, value: number) => ({
    x: padLeft + index * step,
    y: padTop + plotHeight - (Math.max(0, Math.min(100, value)) / 100) * plotHeight,
  })
  const path = data
    .map((point, index) => ({ point, index }))
    .filter(({ point }) => point.activeItems > 0)
    .map(({ point, index }, order) => {
      const { x, y } = pointAt(index, point.averageProgress)
      return `${order === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    }).join(' ')
  const stride = labelStride(data.length)
  const lastIndex = data.map(point => point.activeItems > 0).lastIndexOf(true)
  const last = reported[reported.length - 1]
  const lastPoint = last ? pointAt(lastIndex, last.averageProgress) : undefined

  return <ChartFrame>
    <svg viewBox={`0 0 ${width} ${height}`} width="100%" role="img" aria-label="주차별 평균 진척도">
      {[0, 50, 100].map(tick => {
        const y = padTop + plotHeight - (tick / 100) * plotHeight
        return <g key={tick}>
          <line x1={padLeft} x2={width - padRight} y1={y} y2={y} stroke={gridInk} strokeWidth={1} />
          <text x={padLeft - 7} y={y + 4} textAnchor="end" fontSize={10} fill={axisInk}>{tick}</text>
        </g>
      })}
      {reported.length > 1 && <path d={path} fill="none" stroke={stateColors.progress} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />}
      {data.map((point, index) => {
        if (point.activeItems <= 0) return null
        const { x, y } = pointAt(index, point.averageProgress)
        return <g key={point.weekStart} {...bind(
          <><strong>{point.weekStart}</strong><span>평균 진척도 {point.averageProgress}%</span><span>수행 업무 {point.activeItems}건</span></>
        )}>
          <circle cx={x} cy={y} r={9} fill="transparent" />
          <circle cx={x} cy={y} r={4} fill={stateColors.progress} stroke="#fff" strokeWidth={2} />
        </g>
      })}
      {/* Only the endpoint is direct-labelled, and it sits on its own point so
          the number is never read against the wrong week. */}
      {last && lastPoint && <text
        x={Math.min(width - padRight, lastPoint.x + 9)}
        y={Math.max(padTop + 8, lastPoint.y - 9)}
        textAnchor={lastPoint.x > width - padRight - 40 ? 'end' : 'start'}
        fontSize={11} fontWeight={700} fill="#334155">{last.averageProgress}%</text>}
      <line x1={padLeft} x2={width - padRight} y1={padTop + plotHeight} y2={padTop + plotHeight} stroke={axisInk} strokeWidth={1} />
      {data.map((point, index) => index % stride === 0
        ? <text key={point.weekStart} x={padLeft + index * step} y={height - 10}
            textAnchor="middle" fontSize={10} fill={axisInk}>{shortWeek(point.weekStart)}</text>
        : null)}
    </svg>
    {node}
  </ChartFrame>
}

export interface TimelineTask {
  title: string
  category: string
  firstWeek: string
  lastWeek: string
  progress: number
  weeks: { weekStart: string; progress: number; hasIssue: boolean }[]
  atRisk: boolean
  stalled: boolean
  completed: boolean
}

/**
 * Task timeline. Each row is one de-duplicated task drawn across the reporting
 * weeks of the period, so a reader sees at a glance which work ran all period,
 * which appeared once, and where issues clustered.
 */
export function TaskTimeline({ weeks, tasks }: { weeks: string[]; tasks: TimelineTask[] }) {
  const { bind, node } = useTooltip()
  const rowHeight = 26, labelWidth = 210, padRight = 46, padTop = 26
  const width = 760
  const height = padTop + tasks.length * rowHeight + 10
  const plotWidth = width - labelWidth - padRight
  const column = plotWidth / Math.max(1, weeks.length)
  const stride = labelStride(weeks.length, 10)
  const indexOf = (week: string) => Math.max(0, weeks.indexOf(week))

  const taskColor = (task: TimelineTask) => {
    if (task.completed) return stateColors.completed
    if (task.atRisk) return stateColors.risk
    if (task.stalled) return stateColors.stalled
    return stateColors.progress
  }

  return <ChartFrame>
    <div className="chart-scroll">
      <svg viewBox={`0 0 ${width} ${height}`} width="100%" style={{ minWidth: Math.max(560, weeks.length * 26 + labelWidth) }} role="img" aria-label="기간 내 업무 수행 타임라인">
        {weeks.map((week, index) => <g key={week}>
          <line x1={labelWidth + index * column} x2={labelWidth + index * column} y1={padTop - 6} y2={height - 6} stroke={gridInk} strokeWidth={1} />
          {index % stride === 0 && <text x={labelWidth + index * column + column / 2} y={padTop - 12}
            textAnchor="middle" fontSize={10} fill={axisInk}>{shortWeek(week)}</text>}
        </g>)}
        {tasks.map((task, row) => {
          const y = padTop + row * rowHeight
          const start = indexOf(task.firstWeek)
          const end = indexOf(task.lastWeek)
          const x = labelWidth + start * column
          const spanWidth = Math.max(column * 0.6, (end - start + 1) * column - 3)
          const color = taskColor(task)
          const state = task.atRisk ? '이슈 지속' : task.stalled ? '정체' : task.completed ? '완료' : '진행'
          return <g key={task.title + row} {...bind(
            <><strong>{task.title}</strong><span>{state} · 진척도 {task.progress}%</span>
              <span>{task.firstWeek} ~ {task.lastWeek} · {task.weeks.length}개 주차</span></>
          )}>
            <rect x={0} y={y} width={width} height={rowHeight} fill="transparent" />
            <text x={0} y={y + 15} fontSize={11} fill="#334155">
              {task.title.length > 17 ? `${task.title.slice(0, 17)}…` : task.title}
            </text>
            <rect x={x} y={y + 5} width={spanWidth} height={11} rx={4} fill={color} opacity={0.18} />
            <rect x={x} y={y + 5} width={Math.max(3, spanWidth * (task.progress / 100))} height={11} rx={4} fill={color} />
            {task.weeks.filter(entry => entry.hasIssue).map(entry =>
              <circle key={entry.weekStart} cx={labelWidth + indexOf(entry.weekStart) * column + column / 2}
                cy={y + 10.5} r={3} fill={stateColors.risk} stroke="#fff" strokeWidth={1.5} />)}
            <text x={width - padRight + 8} y={y + 15} fontSize={10} fontWeight={700} fill="#475569">{task.progress}%</text>
          </g>
        })}
      </svg>
    </div>
    {node}
    <Legend items={[
      { label: '완료', color: stateColors.completed },
      { label: '진행', color: stateColors.progress },
      { label: '정체', color: stateColors.stalled },
      { label: '이슈 지속 · 이슈 발생 주차', color: stateColors.risk },
    ]} />
  </ChartFrame>
}

/**
 * Portfolio composition: where the period's work actually went. A single
 * part-to-whole bar with direct labels, folding the tail into 기타 so no more
 * than six colours ever carry meaning.
 */
export function CompositionBar({ items }: { items: { name: string; value: number }[] }) {
  const { bind, node } = useTooltip()
  const total = items.reduce((sum, item) => sum + item.value, 0)
  if (total <= 0) return null
  const head = items.slice(0, 5)
  const tail = items.slice(5)
  const segments = tail.length
    ? [...head, { name: `기타 ${tail.length}개`, value: tail.reduce((sum, item) => sum + item.value, 0) }]
    : head

  return <ChartFrame>
    <div className="composition-bar">
      {segments.map((segment, index) => {
        const share = (segment.value / total) * 100
        return <span key={segment.name} style={{ width: `${share}%`, background: seriesColors[index] }}
          {...bind(<><strong>{segment.name}</strong><span>{segment.value}건 · {share.toFixed(1)}%</span></>)} />
      })}
    </div>
    {node}
    <ul className="composition-legend">
      {segments.map((segment, index) => <li key={segment.name}>
        <i style={{ background: seriesColors[index] }} />
        <span>{segment.name}</span>
        <strong>{segment.value}건</strong>
        <small>{((segment.value / total) * 100).toFixed(1)}%</small>
      </li>)}
    </ul>
  </ChartFrame>
}

/** Horizontal ranking bars. One measure, one hue — length carries the value. */
export function RankBars({ items, unit = '건' }: { items: { name: string; value: number; note?: string }[]; unit?: string }) {
  const maximum = Math.max(1, ...items.map(item => item.value))
  return <div className="rank-bars">
    {items.map(item => <div key={item.name} className="rank-row">
      <span title={item.name}>{item.name}</span>
      <div><i style={{ width: `${(item.value / maximum) * 100}%` }} /></div>
      <strong>{item.value}{unit}</strong>
      {item.note && <small>{item.note}</small>}
    </div>)}
  </div>
}
