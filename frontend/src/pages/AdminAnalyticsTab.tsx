import { useEffect, useMemo, useState } from 'react'
import { errorText, api } from '../api'
import { Modal, Card, Empty, Spinner } from '../components'
import { ProgressTrendChart, RankBars } from '../charts'
import WordCloud, { termTrend, trendColors } from '../WordCloud'
import type { AnalysisTerm, KeywordAnalytics, OrganizationAnalytics, ParticipationAnalytics } from '../types'

const fields = [
  { key: 'ALL', name: '전체' }, { key: 'TITLE', name: '업무명' },
  { key: 'CURRENT', name: '금주 실적' }, { key: 'NEXT', name: '차주 계획' }, { key: 'ISSUE', name: '이슈' },
] as const

const ranges = [
  { weeks: 4, name: '4주' }, { weeks: 12, name: '12주' }, { weeks: 26, name: '26주' }, { weeks: 52, name: '52주' },
]

export default function AdminAnalyticsTab({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [weeks, setWeeks] = useState(12)
  const [field, setField] = useState<typeof fields[number]['key']>('ALL')
  const [keywords, setKeywords] = useState<KeywordAnalytics>()
  const [organizations, setOrganizations] = useState<OrganizationAnalytics>()
  const [participation, setParticipation] = useState<ParticipationAnalytics>()
  const [selected, setSelected] = useState<AnalysisTerm>()
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let stale = false
    setLoading(true)
    Promise.all([
      api<KeywordAnalytics>(`/api/v1/admin/analytics/keywords?weeks=${weeks}&field=${field}`),
      api<OrganizationAnalytics>(`/api/v1/admin/analytics/organizations?weeks=${weeks}`),
      api<ParticipationAnalytics>(`/api/v1/admin/analytics/participation?weeks=${weeks}`),
    ]).then(([k, o, p]) => {
      if (stale) return
      setKeywords(k); setOrganizations(o); setParticipation(p)
    }).catch(error => {
      if (!stale) notify(errorText(error, '분석을 불러올 수 없습니다.'), 'error')
    }).finally(() => { if (!stale) setLoading(false) })
    return () => { stale = true }
  }, [weeks, field])

  const topTerms = useMemo(() => (keywords?.terms ?? []).slice(0, 25), [keywords])
  // Rising is only meaningful against a real baseline, and the candidates are
  // taken from the weight-ranked head so a generic high-frequency word cannot
  // dominate the list purely by volume.
  const hasBaseline = (keywords?.comparedReports ?? 0) > 0
  const rising = useMemo(() => !hasBaseline ? [] : (keywords?.terms ?? [])
    .slice(0, 40).filter(term => term.delta > 0)
    .sort((a, b) => b.delta - a.delta).slice(0, 8), [keywords, hasBaseline])

  return <>
    <div className="analytics-controls">
      <div className="tabs rollup-tabs">
        {ranges.map(range => <button key={range.weeks} className={weeks === range.weeks ? 'active' : ''}
          onClick={() => setWeeks(range.weeks)}>{range.name}</button>)}
      </div>
      <label>분석 대상<select value={field} onChange={event => setField(event.target.value as typeof field)}>
        {fields.map(item => <option key={item.key} value={item.key}>{item.name}</option>)}
      </select></label>
      {keywords && <span className="muted">{keywords.start} ~ {keywords.end} · 보고서 {keywords.reports}건</span>}
    </div>

    {loading ? <Spinner /> : <>
      <Card title="업무 키워드" action={keywords && <span className="muted-chip">
        직전 {keywords.weeks}주({keywords.comparedStart} ~ {keywords.comparedEnd}) 대비 · 보고서 {keywords.comparedReports}건
      </span>}>
        <p className="muted">보고서 한 건을 문서 하나로 보고, 모든 보고서에 나오는 상투적인 표현은 가중치를 낮춥니다. 크기는 가중치{hasBaseline ? ', 색은 직전 기간 대비 변화' : ''}입니다. 정확한 수치는 오른쪽 표를 확인하십시오.</p>
        {!keywords?.terms.length ? <Empty>이 기간에 분석할 보고 텍스트가 없습니다.</Empty> : <div className="analytics-split">
          <WordCloud terms={keywords.terms} onSelect={setSelected} showTrend={hasBaseline} />
          <div className="term-table">
            <div className="table-wrap"><table>
              <thead><tr><th>#</th><th>키워드</th><th>빈도</th><th>보고서</th><th>변화</th></tr></thead>
              <tbody>{topTerms.map((term, index) => <tr key={term.term}
                className={selected?.term === term.term ? 'selected' : ''} onClick={() => setSelected(term)}>
                <td className="rank-cell">{index + 1}</td>
                <td><span className="term-dot" style={{ background: trendColors[termTrend(term)] }} />
                  {term.term}{term.phrase && <small className="cell-sub">복합어</small>}
                  {term.variants?.length ? <small className="cell-sub" title={term.variants.join(', ')}>
                    띄어쓰기 다른 표기 {term.variants.length}건 합산</small> : null}</td>
                <td>{term.count}</td>
                <td>{term.documents}</td>
                <td className={term.delta > 0 ? 'term-up' : term.delta < 0 ? 'term-down' : ''}>
                  {term.delta > 0 ? `+${term.delta}` : term.delta}</td>
              </tr>)}</tbody>
            </table></div>
          </div>
        </div>}
      </Card>

      <Card title="급상승 키워드">
        <p className="muted">직전 동일 기간과 비교해 언급이 가장 많이 늘어난 항목입니다. 새로 시작된 일이나 확산 중인 이슈를 먼저 확인하십시오.</p>
        {!hasBaseline ? <Empty>비교할 직전 기간({keywords?.comparedStart} ~ {keywords?.comparedEnd})에 보고서가 없어 증감을 계산할 수 없습니다.</Empty>
          : !rising.length ? <Empty>직전 기간 대비 뚜렷하게 늘어난 키워드가 없습니다.</Empty> : ''}
        {rising.length > 0 && <RankBars unit="회" items={rising.map(term => ({
          name: term.term, value: term.delta,
          note: `현재 ${term.count}회 · 보고서 ${term.documents}건${term.delta >= term.count ? ' · 신규' : ''}`,
        }))} />}
      </Card>

      <Card title="조직별 보고 현황" action={organizations && <span className="muted-chip">{organizations.weeks}주 누계</span>}>
        {!organizations?.organizations.length ? <Empty>조직 정보가 없습니다.</Empty> : <div className="table-wrap"><table>
          <thead><tr><th>조직</th><th>인원</th><th>제출률</th><th>업무</th><th>완료율</th><th>이슈율</th><th>평균 진척도</th></tr></thead>
          <tbody>{organizations.organizations.map(org => <tr key={org.organizationId}>
            <td><strong>{org.name}</strong></td>
            <td>{org.members}</td>
            <td><div className="cell-progress"><i style={{ width: `${Math.min(100, org.submissionRate)}%` }} /></div>
              <small>{org.submissionRate.toFixed(0)}%</small>
              <small className="cell-sub">{org.reports} / {org.expectedReports}건</small></td>
            <td>{org.items}</td>
            <td>{org.completionRate.toFixed(0)}%</td>
            <td className={org.issueRate > 25 ? 'danger-text' : ''}>{org.issueRate.toFixed(0)}%</td>
            <td>{org.averageProgress.toFixed(0)}%</td>
          </tr>)}</tbody>
        </table></div>}
      </Card>

      <Card title="보고 참여 추이">
        <p className="muted">제출률이 낮은 구간의 집계 결과는 그만큼 신뢰도가 낮습니다. 다른 지표를 읽기 전에 먼저 확인하십시오.</p>
        {!participation?.trend.length ? <Empty>표시할 주차가 없습니다.</Empty> : <>
          <ProgressTrendChart data={participation.trend.map(week => ({
            weekStart: week.weekStart, averageProgress: week.submissionRate, activeItems: week.reports,
          }))} />
          <div className="metric-grid analytics-metrics">
            <Card><span className="metric-label">활성 사용자</span><strong className="metric-value">{participation.activeUsers}</strong><small>보고 대상 인원</small></Card>
            <Card><span className="metric-label">최근 주 제출률</span><strong className="metric-value">
              {(participation.trend[participation.trend.length - 1]?.submissionRate ?? 0).toFixed(0)}%</strong>
              <small>{participation.trend[participation.trend.length - 1]?.submitted ?? 0}명 제출</small></Card>
            <Card><span className="metric-label">기한 내 제출</span><strong className="metric-value">
              {(participation.trend[participation.trend.length - 1]?.onTimeRate ?? 0).toFixed(0)}%</strong>
              <small>{participation.deadline?.label ?? '기준 미설정'}</small></Card>
            <Card><span className="metric-label">미제출 인원</span><strong className="metric-value">{participation.missing.length}</strong><small>기간 내 1회 이상 누락</small></Card>
          </div>
        </>}
      </Card>

      {participation && participation.missing.length > 0 && <Card title="미제출 현황" action={<span className="muted-chip">누락 주차 많은 순 · 최대 25명</span>}>
        <div className="table-wrap"><table>
          <thead><tr><th>이름</th><th>아이디</th><th>조직</th><th>누락 주차</th><th>최근 제출 주차</th></tr></thead>
          <tbody>{participation.missing.map(person => <tr key={person.userId}>
            <td><strong>{person.displayName}</strong></td>
            <td><code>{person.username}</code></td>
            <td>{person.organization || '-'}</td>
            <td className={person.missedWeeks >= participation.weeks ? 'danger-text' : ''}>
              {person.missedWeeks} / {participation.weeks}주</td>
            <td>{person.lastWeek || '기록 없음'}</td>
          </tr>)}</tbody>
        </table></div>
      </Card>}
    </>}

    {selected && <Modal onClose={() => setSelected(undefined)} label={`${selected.term} 키워드 상세`}>
        <header><div><span className="week-label">키워드</span><h2>{selected.term}</h2></div>
          <button onClick={() => setSelected(undefined)}>×</button></header>
        <div className="rollup-detail">
          <div className="rollup-detail-meta">
            <div><small>총 언급</small><strong>{selected.count}회</strong></div>
            <div><small>등장 보고서</small><strong>{selected.documents}건</strong></div>
            <div><small>직전 대비</small><strong>{selected.delta >= 0 ? `+${selected.delta}` : selected.delta}회</strong></div>
            <div><small>형태</small><strong>{selected.phrase ? '복합어' : '단어'}</strong></div>
          </div>
          <p className="muted">빠른 이동(Ctrl+K)에서 이 키워드로 검색하면 해당 문장이 포함된 보고서를 바로 열 수 있습니다.</p>
        </div>
    </Modal>}
  </>
}
