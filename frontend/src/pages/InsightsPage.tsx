import { useEffect, useState } from 'react'
import { api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import type { WorkGraphView, WorkLink } from '../types'

/**
 * Cross-team work insight: who else is doing something like this, which teams
 * are connected by which subjects, and which work is routine operation rather
 * than a project.
 *
 * Every finding here is a candidate for a person to confirm, never a fact the
 * system asserts. Two teams with matching titles may be doing genuinely
 * different work, so each row carries the reason it was surfaced and the
 * evidence behind it.
 */

const tabs = [
  { key: 'DUPLICATE', name: '중복 의심', hint: '서로 다른 조직에서 같은 기간에 진행 중인 유사 업무입니다. 중복 투자 여부를 확인하세요.' },
  { key: 'SIMILAR', name: '유사 업무', hint: '참고하거나 협업할 수 있는 다른 담당자의 업무입니다.' },
  { key: 'COLLAB', name: '협업 지도', hint: '어떤 조직이 어떤 주제로 연결돼 있는지 보여줍니다. 소통 이력이 아니라 업무 주제의 연결입니다.' },
  { key: 'RECURRING', name: '반복 업무', hint: '완료를 향해 움직이지 않고 일정한 주기로 계속 보고되는 운영성 업무입니다.' },
] as const
type TabKey = typeof tabs[number]['key']

function LinkCard({ link }: { link: WorkLink }) {
  return <li className={link.duplicateCandidate ? 'link-row duplicate' : 'link-row'}>
    <div className="link-pair">
      <div>
        <strong>{link.left.title}</strong>
        <small>{link.left.displayName} · {link.left.organizationName || '조직 미지정'} · 진척 {link.left.progress}%</small>
      </div>
      <span className="link-score">{link.similarity}%</span>
      <div>
        <strong>{link.right.title}</strong>
        <small>{link.right.displayName} · {link.right.organizationName || '조직 미지정'} · 진척 {link.right.progress}%</small>
      </div>
    </div>
    <p className="muted">{link.reason}</p>
    <div className="link-terms">{link.sharedTerms.map(term => <span key={term} className="muted-chip">{term}</span>)}</div>
  </li>
}

export default function InsightsPage({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [weeks, setWeeks] = useState(12)
  const [tab, setTab] = useState<TabKey>('DUPLICATE')
  const [view, setView] = useState<WorkGraphView>()

  useEffect(() => {
    let stale = false
    setView(undefined)
    api<WorkGraphView>(`/api/v1/insights/work-graph?weeks=${weeks}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ weeks, since: '', workItems: 0, similar: [], duplicates: [], collaboration: [], recurring: [] })
        notify(error instanceof Error ? error.message : '업무 인사이트를 불러올 수 없습니다.', 'error')
      })
    return () => { stale = true }
  }, [weeks])

  const active = tabs.find(entry => entry.key === tab)!

  return <>
    <PageHeader title="업무 인사이트" description="조직을 가로질러 같은 일이 어디서 벌어지고 있는지 확인합니다."
      action={<select value={weeks} onChange={event => setWeeks(Number(event.target.value))}>
        {[4, 8, 12, 26, 52].map(value => <option key={value} value={value}>최근 {value}주</option>)}
      </select>} />

    <div className="tab-row">
      {tabs.map(entry => <button key={entry.key} className={tab === entry.key ? 'tab active' : 'tab'}
        onClick={() => setTab(entry.key)}>{entry.name}
        {view && <small>{entry.key === 'DUPLICATE' ? view.duplicates.length
          : entry.key === 'SIMILAR' ? view.similar.length
          : entry.key === 'COLLAB' ? view.collaboration.length : view.recurring.length}</small>}
      </button>)}
    </div>

    {view === undefined ? <Spinner/> : <Card title={active.name} action={<span className="muted-chip">업무 {view.workItems}건 분석</span>}>
      <p className="muted">{active.hint}</p>

      {tab === 'DUPLICATE' && (view.duplicates.length === 0
        ? <Empty>조직 간 중복으로 의심되는 업무가 없습니다.</Empty>
        : <ul className="link-list">{view.duplicates.map((link, index) =>
            <LinkCard key={`${link.left.workItemId}-${link.right.workItemId}-${index}`} link={link} />)}</ul>)}

      {tab === 'SIMILAR' && (view.similar.length === 0
        ? <Empty>연결할 유사 업무가 없습니다.</Empty>
        : <ul className="link-list">{view.similar.map((link, index) =>
            <LinkCard key={`${link.left.workItemId}-${link.right.workItemId}-${index}`} link={link} />)}</ul>)}

      {tab === 'COLLAB' && (view.collaboration.length === 0
        ? <Empty>조직 간 연결된 업무가 없습니다.</Empty>
        : <div className="table-wrap"><table>
            <thead><tr><th>조직</th><th>조직</th><th>연결 업무</th><th>인원</th><th>주제</th></tr></thead>
            <tbody>{view.collaboration.map(edge => <tr key={`${edge.leftOrganization}-${edge.rightOrganization}`}>
              <td>{edge.leftOrganization}</td><td>{edge.rightOrganization}</td>
              <td><strong>{edge.sharedWork}</strong></td><td>{edge.people}</td>
              <td>{edge.topics.map(topic => <span key={topic} className="muted-chip">{topic}</span>)}</td>
            </tr>)}</tbody>
          </table></div>)}

      {tab === 'RECURRING' && (view.recurring.length === 0
        ? <Empty>반복 운영 업무로 분류된 항목이 없습니다.</Empty>
        : <div className="table-wrap"><table>
            <thead><tr><th>업무</th><th>담당</th><th>보고</th><th>주기</th><th>진척 변화</th><th>판정 근거</th></tr></thead>
            <tbody>{view.recurring.map(item => <tr key={item.workItemId}>
              <td><strong>{item.title}</strong></td>
              <td>{item.displayName}<small className="cell-sub">{item.organizationName}</small></td>
              <td>{item.reportedWeeks}/{item.ageWeeks}주</td>
              <td>{item.cadencePercent}%</td>
              <td>{item.progressGain > 0 ? `+${item.progressGain}` : item.progressGain}%</td>
              <td className="muted">{item.reason}</td>
            </tr>)}</tbody>
          </table></div>)}
    </Card>}
  </>
}
