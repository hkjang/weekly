import { useEffect, useState } from 'react'
import { errorText, api } from '../api'
import { Card, Empty, PageHeader, Spinner } from '../components'
import { CollaborationMatrix } from '../charts'
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
  { key: 'BOTTLENECK', name: '병목', hint: '여러 업무가 하나의 선행 업무를 기다리고 있습니다. 추측이 아니라 담당자가 직접 등록한 관계입니다.' },
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
  const [showCollabTable, setShowCollabTable] = useState(false)

  useEffect(() => {
    let stale = false
    setView(undefined)
    api<WorkGraphView>(`/api/v1/insights/work-graph?weeks=${weeks}`)
      .then(value => { if (!stale) setView(value) })
      .catch(error => {
        if (stale) return
        setView({ weeks, since: '', workItems: 0, similar: [], similarTotal: 0, duplicates: [], duplicateTotal: 0, collaboration: [], recurring: [], bottlenecks: [] })
        notify(errorText(error, '업무 인사이트를 불러올 수 없습니다.'), 'error')
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
        {view && <small>{entry.key === 'DUPLICATE' ? view.duplicateTotal
          : entry.key === 'SIMILAR' ? view.similarTotal
          : entry.key === 'COLLAB' ? view.collaboration.length
          : entry.key === 'BOTTLENECK' ? view.bottlenecks.length : view.recurring.length}</small>}
      </button>)}
    </div>

    {view === undefined ? <Spinner/> : <Card title={active.name} action={<span className="muted-chip">업무 {view.workItems}건 분석</span>}>
      <p className="muted">{active.hint}</p>

      {tab === 'DUPLICATE' && (view.duplicates.length === 0
        ? <Empty>조직 간 중복으로 의심되는 업무가 없습니다.</Empty>
        : <><Capped shown={view.duplicates.length} total={view.duplicateTotal} />
          <ul className="link-list">{view.duplicates.map((link, index) =>
            <LinkCard key={`${link.left.workItemId}-${link.right.workItemId}-${index}`} link={link} />)}</ul></>)}

      {tab === 'SIMILAR' && (view.similar.length === 0
        ? <Empty>연결할 유사 업무가 없습니다.</Empty>
        : <><Capped shown={view.similar.length} total={view.similarTotal} />
          <ul className="link-list">{view.similar.map((link, index) =>
            <LinkCard key={`${link.left.workItemId}-${link.right.workItemId}-${index}`} link={link} />)}</ul></>)}

      {tab === 'COLLAB' && (view.collaboration.length === 0
        ? <Empty>조직 간 연결된 업무가 없습니다.</Empty>
        : <>
          <CollaborationMatrix edges={view.collaboration} />
          <button className="link-button" onClick={() => setShowCollabTable(current => !current)}>
            {showCollabTable ? '표 접기' : `표로 보기 (${view.collaboration.length}쌍)`}
          </button>
          {showCollabTable && <div className="table-wrap"><table>
            <thead><tr><th>조직</th><th>조직</th><th>연결 업무</th><th>인원</th><th>주제</th></tr></thead>
            <tbody>{view.collaboration.map(edge => <tr key={`${edge.leftOrganization}-${edge.rightOrganization}`}>
              <td>{edge.leftOrganization}</td><td>{edge.rightOrganization}</td>
              <td><strong>{edge.sharedWork}</strong></td><td>{edge.people}</td>
              <td>{edge.topics.map(topic => <span key={topic} className="muted-chip">{topic}</span>)}</td>
            </tr>)}</tbody>
          </table></div>}
        </>)}

      {tab === 'BOTTLENECK' && (view.bottlenecks.length === 0
        ? <Empty>여러 업무를 막고 있는 선행 업무가 없습니다. 다른 업무를 기다리고 있다면 업무 추적에서 선행 관계를 등록하세요.</Empty>
        : <ul className="bottleneck-list">{view.bottlenecks.map(item => <li key={item.workItemId} className="bottleneck">
            <div className="bottleneck-head">
              <strong>{item.title}</strong>
              <span className="state-chip stalled">{item.blocked}건 대기</span>
              {item.crossOrganization > 0 && <span className="state-chip risk">타 조직 {item.crossOrganization}건</span>}
            </div>
            <div className="decision-facts">
              <span>{item.displayName}{item.organizationName ? ` · ${item.organizationName}` : ''}</span>
              <span>진척 {item.progress}%</span>
              <span>{item.lastWeek ? `최근 ${item.lastWeek}` : '보고 없음'}</span>
            </div>
            <p className="bottleneck-waiting">기다리는 업무: {item.waiting.join(' · ')}
              {item.blocked > item.waiting.length ? ` 외 ${item.blocked - item.waiting.length}건` : ''}</p>
          </li>)}</ul>)}

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

/**
 * Says when a list is showing only part of what matched.
 *
 * The endpoint returns the strongest links rather than all of them — every pair
 * was once serialized, which for 1,805 tasks meant 1.6 million entries — so the
 * screen has to say what it is leaving out instead of looking complete.
 */
function Capped({ shown, total }: { shown: number; total: number }) {
  if (total <= shown) return null
  return <p className="muted capped-note">
    조건에 맞는 {total.toLocaleString()}건 중 관련도가 높은 {shown.toLocaleString()}건만 보여 줍니다.
    범위를 좁히려면 위의 기간을 줄이세요.
  </p>
}
