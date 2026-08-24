import { useState } from 'react'
import type { ReactNode } from 'react'
import { localDate } from './localdate'

/**
 * Inline SVG chart primitives. The app ships under a strict CSP with no external
 * script or style hosts, so every chart here is hand drawn with plain SVG and the
 * existing design tokens. No chart runs on a second y-axis: a chart shows one
 * measure on one scale, and a second measure gets its own chart beside it.
 */

/**
 * The colour vocabulary lives in styles.css :root, so the SVG here and the
 * status badges elsewhere cannot drift apart. One hue family carries one
 * meaning across the whole app: 회색 멈춤, 파랑 진행, 녹색 완료, 주황 정체,
 * 빨강 문제.
 *
 * Series slots are a single violet ramp ordered largest share to smallest, in a
 * family no state uses. A share of a whole is not a state, and used to be drawn
 * in the same blue as 진행 and the same amber as 정체.
 */
export const seriesColors = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)',
  'var(--series-4)', 'var(--series-5)', 'var(--series-6)']

export const stateColors = {
  completed: 'var(--state-completed)',
  progress: 'var(--state-progress)',
  notStarted: 'var(--state-not-started)',
  stalled: 'var(--state-stalled)',
  risk: 'var(--state-risk)',
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

/**
 * A task's week-by-week record, drawn as one column per week.
 *
 * The handover screen listed milestones, which by design skips the weeks where
 * nothing changed — right for a reading list, wrong for the question a new
 * owner is actually asking. "언제부터, 얼마나 자주, 어디서 멈췄나" is a question
 * about the shape of the whole record, including the weeks with nothing in
 * them, and a list of turning points cannot show a gap.
 *
 * Each column carries three facts with one mark: whether the week was reported
 * at all, how far the work had got, and whether an issue was open. The colours
 * are the app's, so a stalled week here is the same amber as a stalled task
 * anywhere else.
 */
export function WeekTrack({ firstWeek, lastWeek, track }: {
  firstWeek: string; lastWeek: string
  track: { week: string; progress: number; issue?: boolean }[]
}) {
  const { bind, node } = useTooltip()
  const axis = weekAxis(firstWeek, lastWeek, track.map(entry => entry.week))
  if (axis.length === 0) return null
  const reported = new Map(track.map(entry => [entry.week, entry]))

  const width = 760, padTop = 12, plotHeight = 46, labelRow = 16
  const height = padTop + plotHeight + labelRow
  const column = width / axis.length
  const barWidth = Math.max(3, Math.min(18, column - 3))
  const stride = labelStride(axis.length, 12)

  // A week's colour is the app's vocabulary applied to that week alone: an open
  // issue is a problem, no movement since the previous reported week is 정체,
  // anything else is 진행, and a finished task is 완료.
  let previous = -1
  const columns = axis.map((week, index) => {
    const entry = reported.get(week)
    if (!entry) return { week, index, entry: undefined, color: '' }
    const color = entry.issue ? stateColors.risk
      : entry.progress >= 100 ? stateColors.completed
      : previous >= 0 && entry.progress <= previous ? stateColors.stalled
      : stateColors.progress
    previous = entry.progress
    return { week, index, entry, color }
  })
  const silent = columns.filter(item => !item.entry).length
  const used = new Set(columns.filter(item => item.entry).map(item => item.color))
  const truncated = track.filter(entry => !axis.includes(entry.week)).length

  return <ChartFrame>
    <div className="chart-scroll">
      <svg viewBox={`0 0 ${width} ${height}`} width="100%" style={{ minWidth: Math.max(280, axis.length * 15) }}
        role="img" aria-label={`주차별 기록 ${axis.length}주 중 ${track.length}주 보고`}>
        <line x1={0} x2={width} y1={padTop + plotHeight} y2={padTop + plotHeight} stroke={gridInk} strokeWidth={1} />
        {columns.map(({ week, index, entry, color }) => {
          const x = index * column + (column - barWidth) / 2
          const label = <><strong>{week}</strong>
            {entry
              ? <><span>진척도 {entry.progress}%</span>{entry.issue && <span>이슈 있음</span>}</>
              : <span>보고 없음</span>}</>
          if (!entry) {
            return <g key={week} {...bind(label)}>
              <rect x={index * column} y={padTop} width={column} height={plotHeight} fill="transparent" />
              <rect x={x} y={padTop + 2} width={barWidth} height={plotHeight - 4} rx={2}
                fill="none" stroke={stateColors.notStarted} strokeWidth={1} strokeDasharray="2 3" opacity={0.45} />
            </g>
          }
          const barHeight = Math.max(2, (plotHeight - 6) * (entry.progress / 100))
          return <g key={week} {...bind(label)}>
            <rect x={index * column} y={padTop} width={column} height={plotHeight} fill="transparent" />
            <rect x={x} y={padTop + 6} width={barWidth} height={plotHeight - 6} rx={2} fill={color} opacity={0.14} />
            <rect x={x} y={padTop + plotHeight - barHeight} width={barWidth} height={barHeight} rx={2} fill={color} />
            {entry.issue && <rect x={x + barWidth / 2 - 2} y={padTop - 6} width={4} height={4} fill={stateColors.risk} />}
          </g>
        })}
        {axis.map((week, index) => index % stride === 0 && <text key={week}
          x={index * column + column / 2} y={height - 4} textAnchor="middle" fontSize={9} fill={axisInk}>
          {shortWeek(week)}
        </text>)}
      </svg>
    </div>
    {node}
    <Legend items={[
      // Only the states this task actually went through. A legend naming
      // colours that are not on the chart is a legend nobody reads.
      ...[{ label: '진행', color: stateColors.progress },
        { label: '정체', color: stateColors.stalled },
        { label: '이슈', color: stateColors.risk },
        { label: '완료', color: stateColors.completed }].filter(item => used.has(item.color)),
      ...(silent > 0 ? [{ label: `보고 없음 ${silent}주`, color: stateColors.notStarted }] : []),
    ]} />
    {truncated > 0 && <p className="muted chart-note">
      기록이 {axis.length}주를 넘어 앞부분 {truncated}주는 그리지 않았습니다.
    </p>}
  </ChartFrame>
}

/**
 * Every week from the first to the last, including the ones nobody reported.
 *
 * The reported weeks are merged in rather than assumed to sit on the seven-day
 * grid. Reports do not all start on the same weekday — the week start is a
 * setting, and v0.23 shipped a report that finds the ones that disagree — so a
 * stepped axis alone would silently drop the weeks that fall between steps.
 *
 * Bounded, keeping the tail: a task running longer than two years is handed
 * over on what it has been doing lately, and the caller says how many weeks
 * were dropped rather than quietly showing a shorter history.
 */
function weekAxis(firstWeek: string, lastWeek: string, reported: string[]) {
  const start = Date.parse(`${firstWeek}T00:00:00Z`)
  const end = Date.parse(`${lastWeek}T00:00:00Z`)
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return [...reported].sort()
  const weeks = new Set(reported)
  for (let time = start; time <= end && weeks.size < 400; time += 7 * 86400000) {
    weeks.add(localDate(new Date(time)))
  }
  const sorted = [...weeks].sort()
  return sorted.length > 105 ? sorted.slice(sorted.length - 105) : sorted
}

/**
 * Which organisations are working near each other, as a matrix.
 *
 * The tab is called 협업 지도 and used to be a table of one row per pair. That
 * is C(n,2) rows — twelve organisations produced sixty-six — and answering
 * "who is my organisation working alongside" meant reading every row and
 * remembering which ones mentioned you. A matrix answers it with one row: the
 * dark cells in your row are your neighbours.
 *
 * A node-and-link diagram is the other obvious choice and is worse here. Twelve
 * nodes with sixty-six edges is a ball of yarn, and the layout would have to be
 * computed; a grid never overlaps itself and is rectangles.
 *
 * Violet carries the magnitude because no delivery state uses violet, so a dark
 * cell reads as "a lot", never as "a problem".
 */
export function CollaborationMatrix({ edges, limit = 20 }: {
  edges: { leftOrganization: string; rightOrganization: string; sharedWork: number; people: number; topics: string[] }[]
  limit?: number
}) {
  const { bind, node } = useTooltip()
  // An organisation with no name still occupies a row; leaving the label blank
  // would put an anonymous line in the middle of the map.
  const named = (name: string) => name.trim() || '조직 미지정'
  const totals = new Map<string, number>()
  for (const edge of edges) {
    totals.set(named(edge.leftOrganization), (totals.get(named(edge.leftOrganization)) ?? 0) + edge.sharedWork)
    totals.set(named(edge.rightOrganization), (totals.get(named(edge.rightOrganization)) ?? 0) + edge.sharedWork)
  }
  // Busiest first, so the top-left corner is where the reading starts.
  const ordered = [...totals.entries()].sort((left, right) => right[1] - left[1]).map(([name]) => name)
  const shown = ordered.slice(0, limit)
  const hidden = ordered.length - shown.length
  if (shown.length < 2) return null

  const index = new Map(shown.map((name, position) => [name, position]))
  const cells = new Map<string, { value: number; people: number; topics: string[] }>()
  let maximum = 1
  for (const edge of edges) {
    const row = index.get(named(edge.leftOrganization)), column = index.get(named(edge.rightOrganization))
    if (row === undefined || column === undefined) continue
    const entry = { value: edge.sharedWork, people: edge.people, topics: edge.topics }
    cells.set(`${row}:${column}`, entry)
    cells.set(`${column}:${row}`, entry)
    maximum = Math.max(maximum, edge.sharedWork)
  }

  const cell = 26, gap = 2, headHeight = 20
  // Rows carry the names, columns carry their position. The matrix is
  // symmetric, so column 3 is the organisation on row 3 and repeating the name
  // sideways buys nothing — rotated Korean labels at 26px spacing overlapped
  // each other and still had to be truncated.
  const longest = shown.reduce((most, name) => Math.max(most, name.length), 0)
  const labelWidth = Math.min(190, 34 + Math.min(longest, 16) * 11)
  const width = labelWidth + shown.length * cell
  const height = headHeight + shown.length * cell

  // Drawn at its own size rather than stretched to the card: a cell is a fixed
  // 26px square so a twelve-organisation matrix and a twenty-organisation one
  // read the same, and scaling to 100% width blew a small matrix up to twice
  // the size of everything around it.
  return <ChartFrame>
    <div className="chart-scroll">
      <svg viewBox={`0 0 ${width} ${height}`} width={width} height={height} style={{ maxWidth: '100%' }}
        role="img" aria-label={`조직 간 연결 업무 행렬 ${shown.length}개 조직`}>
        {shown.map((name, column) => <text key={`head-${name}`}
          x={labelWidth + column * cell + cell / 2} y={headHeight - 6} fontSize={10} fill={axisInk}
          textAnchor="middle">{column + 1}</text>)}
        {shown.map((rowName, row) => <g key={`row-${rowName}`}>
          <text x={labelWidth - 9} y={headHeight + row * cell + cell / 2 + 4} textAnchor="end" fontSize={11} fill="#334155">
            <tspan fill={axisInk}>{row + 1}. </tspan>
            {rowName.length > 16 ? `${rowName.slice(0, 16)}…` : rowName}
          </text>
          {shown.map((columnName, column) => {
            const x = labelWidth + column * cell, y = headHeight + row * cell
            if (row === column) {
              return <rect key={columnName} x={x + gap / 2} y={y + gap / 2} width={cell - gap} height={cell - gap}
                rx={3} fill={gridInk} />
            }
            const entry = cells.get(`${row}:${column}`)
            const share = entry ? entry.value / maximum : 0
            return <rect key={columnName} x={x + gap / 2} y={y + gap / 2} width={cell - gap} height={cell - gap} rx={3}
              fill={entry ? seriesColors[0] : '#fff'} stroke={entry ? 'none' : gridInk} strokeWidth={1}
              opacity={entry ? 0.14 + 0.86 * share : 1}
              {...bind(entry
                ? <><strong>{rowName} ↔ {columnName}</strong>
                    <span>연결 업무 {entry.value}건 · 인원 {entry.people}명</span>
                    {entry.topics.length > 0 && <span>{entry.topics.slice(0, 4).join(', ')}</span>}</>
                : <><strong>{rowName} ↔ {columnName}</strong><span>연결된 업무가 없습니다</span></>)} />
          })}
        </g>)}
      </svg>
    </div>
    {node}
    <p className="muted chart-note">
      가로줄은 이름으로, 세로줄은 그 번호로 읽습니다. 진한 칸일수록 연결된 업무가 많고, 가장 많은 쌍이 {maximum}건입니다.
      대각선은 같은 조직이라 비워 둡니다.
      {hidden > 0 && ` 연결 업무가 많은 ${shown.length}개 조직만 그렸습니다. 나머지 ${hidden}개 조직은 아래 표에 있습니다.`}
    </p>
  </ChartFrame>
}

// A digest score is a sum of named parts, so it is drawn as one. The colours
// are the part-to-whole ramp, not the state palette: these are shares of a
// score, and a share is not a state.
const groundColors: Record<string, string> = {
  DECISION: seriesColors[0], ISSUE: seriesColors[1], STALLED: seriesColors[2],
  // A declared block and a deadline the pace does not reach share the stall's
  // colour. All three say the same thing about the work — it does not arrive:
  // stopped, stopped by somebody else, or moving too slowly to land in time —
  // and they belong beside each other in a bar read left to right.
  BLOCKED: seriesColors[2], DEADLINE: seriesColors[2],
  SILENT: seriesColors[3], DUPLICATE: seriesColors[4], DONE: seriesColors[5],
}
export const groundLabels: Record<string, string> = {
  DECISION: '결정 대기', ISSUE: '이슈 지속', STALLED: '진척 정체', BLOCKED: '선행 대기',
  DEADLINE: '기한 위험', SILENT: '보고 누락', DUPLICATE: '중복 의심', DONE: '완료',
}
export function groundColor(kind: string) { return groundColors[kind] ?? seriesColors[5] }

/**
 * A digest entry's score, drawn as the parts it is made of.
 *
 * The response used to carry a total and a list of sentences, so comparing two
 * entries meant reading twelve sentences and doing the arithmetic by hand. The
 * bar's length is the score against the highest score on the screen, so rank is
 * visible without reading, and its segments are why.
 */
export function ScoreStack({ grounds, score, maximum }: {
  grounds: { kind: string; text: string; points: number }[]
  score: number; maximum: number
}) {
  const { bind, node } = useTooltip()
  if (score <= 0 || grounds.length === 0) return null
  const share = maximum > 0 ? (score / maximum) * 100 : 100
  return <div className="chart-frame score-stack">
    <div className="score-track"><div className="score-bar" style={{ width: `${share}%` }}>
      {grounds.map((ground, index) => <span key={`${ground.kind}-${index}`}
        style={{ width: `${(ground.points / score) * 100}%`, background: groundColor(ground.kind) }}
        {...bind(<><strong>{groundLabels[ground.kind] ?? ground.kind} {ground.points}점</strong>
          <span>{ground.text}</span></>)} />)}
    </div></div>
    {node}
  </div>
}

// Weekly change reuses the delivery vocabulary rather than inventing a seventh
// palette: 신규·재개·진척 are all movement and share the 진행 blue, 역행 is a
// problem, 정체 is 정체, 누락 is 멈춤. The three blues sit next to each other in
// the reading order, so the bar reads as one "moved" block, which is the answer
// to the question the card asks.
export const changeColors: Record<string, string> = {
  COMPLETED: stateColors.completed,
  NEW: stateColors.progress, RESUMED: stateColors.progress, PROGRESSED: stateColors.progress,
  REGRESSED: stateColors.risk, STALLED: stateColors.stalled,
  SILENT: stateColors.notStarted, STEADY: stateColors.notStarted, ABSENT: stateColors.notStarted,
}

/**
 * Which way the week went, as one bar.
 *
 * The dashboard listed seven counts side by side, which is the data but not the
 * answer: "완료 3 신규 5 재개 1 진척 12 역행 2 정체 8 누락 4" has to be added up
 * before it says anything. The bar spans the items that changed, so the shape
 * is the answer — a wide amber block is a stalled week however the counts read.
 *
 * The bar covers the changed items, not every reported one. Twelve changes out
 * of two hundred would be six percent of the width, and seven segments inside
 * six percent are invisible. The count of reported work is in the sentence
 * above it, where a proportion belongs.
 */
export function ChangeFlow({ groups }: { groups: { kind: string; title: string; count: number }[] }) {
  const { bind, node } = useTooltip()
  const shown = groups.filter(group => group.count > 0)
  const total = shown.reduce((sum, group) => sum + group.count, 0)
  if (total === 0) return null
  return <div className="change-flow">
    <div className="change-flow-bar">
      {shown.map(group => <span key={group.kind}
        style={{ width: `${(group.count / total) * 100}%`, background: changeColors[group.kind] ?? stateColors.notStarted }}
        {...bind(<><strong>{group.title} {group.count}건</strong>
          <span>변화 {total}건 중 {Math.round((group.count / total) * 100)}%</span></>)} />)}
    </div>
    {node}
  </div>
}
