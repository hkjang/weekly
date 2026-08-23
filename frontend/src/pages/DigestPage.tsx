import { useEffect, useState } from 'react'
import { errorText, api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import { ScoreStack, groundColor, groundLabels } from '../charts'
import type { DigestEntry, DigestKind, DigestView } from '../types'
import type { PageName } from '../router'

/**
 * Executive digest: the few things across the organization worth an
 * executive's attention this period, with the facts each selection rests on.
 *
 * The grounds are not decoration. A summary an executive cannot argue with is
 * a summary they cannot use, so every entry shows exactly what was observed
 * and how it was weighted.
 */

const kindLabel: Record<DigestKind, { name: string; tone: string }> = {
  DECISION: { name: '결정 대기', tone: 'tone-decision' },
  RISK: { name: '위험', tone: 'tone-risk' },
  DUPLICATE: { name: '중복 의심', tone: 'tone-duplicate' },
  PROGRESS: { name: '주요 진전', tone: 'tone-progress' },
}

function DigestCard({ entry, maximum, navigate }: {
  entry: DigestEntry; maximum: number; navigate: (page: PageName) => void
}) {
  const kind = kindLabel[entry.kind]
  return <li className={`digest-entry ${kind.tone}`}>
    <div className="digest-head">
      {/* The headline, not the kind. The server picks one of 결정 대기 · 장기
          이슈 · 진척 정체 · 타 조직 대기 · 중복 투자 의심 · 주요 업무 완료 for
          every entry, and this screen was showing 위험 for four of them — so a
          reader had to read the grounds to find out which kind of risk it was.
          The colour still comes from the coarse kind. */}
      <span className="digest-kind">{entry.headline || kind.name}</span>
      <strong>{entry.title}</strong>
      <span className="muted-chip">{entry.displayName}{entry.organizationName ? ` · ${entry.organizationName}` : ''}</span>
      <span className="digest-score" title="선정 점수">{entry.score}</span>
    </div>
    {entry.detail && <p className="digest-detail">{entry.detail}</p>}
    <ScoreStack grounds={entry.grounds} score={entry.score} maximum={maximum} />
    <ul className="digest-grounds">{entry.grounds.map((ground, index) => <li key={`${ground.kind}-${index}`}>
      <i className="ground-swatch" style={{ background: groundColor(ground.kind) }} />
      <span>{ground.text}</span>
      <small>{groundLabels[ground.kind] ?? ground.kind} +{ground.points}</small>
    </li>)}</ul>
    <button className="link-button" onClick={() => navigate('work')}>업무 추적에서 보기</button>
  </li>
}

export default function DigestPage({ notify, navigate }: {
  notify: (message: string, kind?: 'success' | 'error') => void
  navigate: (page: PageName) => void
}) {
  const [weeks, setWeeks] = useState(8)
  const [view, setView] = useState<DigestView>()

  useEffect(() => {
    let stale = false
    setView(undefined)
    api<DigestView>(`/api/v1/digest?weeks=${weeks}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ weeks, since: '', scope: 'TEAM', considered: 0, entries: [] })
        notify(errorText(error, '경영 요약을 불러올 수 없습니다.'), 'error')
      })
    return () => { stale = true }
  }, [weeks])

  // The bars share one scale, so the top entry fills the track and the rest are
  // read against it. Comparing entries is the point of ranking them.
  const topScore = Math.max(1, ...(view?.entries ?? []).map(entry => entry.score))

  return <>
    <PageHeader title="경영 요약" description="전사 업무 중 지금 확인이 필요한 항목만 선정합니다. 선정 근거를 항상 함께 제시합니다."
      action={<select value={weeks} onChange={event => setWeeks(Number(event.target.value))}>
        {[4, 8, 12, 26].map(value => <option key={value} value={value}>최근 {value}주</option>)}
      </select>} />

    {view === undefined ? <Spinner/> : <Card title={`핵심 ${view.entries.length}건`}
      action={<span className="muted-chip">업무 {view.considered}건 검토 · {view.since} 이후</span>}>
      {view.entries.length === 0
        ? <Empty>선정 기준을 넘는 항목이 없습니다. 열린 결정 요청, 장기 이슈, 진척 정체, 중복 의심, 주요 완료가 모두 없습니다.</Empty>
        : <ul className="digest-list">{view.entries.map(entry =>
            <DigestCard key={entry.workItemId} entry={entry} navigate={navigate} maximum={topScore} />)}</ul>}
    </Card>}
  </>
}
