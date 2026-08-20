import { useEffect, useState } from 'react'
import { api } from '../api'
import { Button, Card, Empty, PageHeader, Spinner, StatusBadge } from '../components'
import type { AnalyticsOverview, ChangeSummary, Report, SessionInfo } from '../types'

export default function DashboardPage({ session, navigate }: { session: SessionInfo; navigate: (page: 'current' | 'team') => void }) {
  const [report, setReport] = useState<Report | null>()
  const [analytics, setAnalytics] = useState<AnalyticsOverview>()
  const [changes, setChanges] = useState<ChangeSummary>()
  useEffect(() => { api<Report | null>('/api/v1/reports/current').then(setReport); api<ChangeSummary>('/api/v1/changes').then(setChanges).catch(() => setChanges(undefined)); if (session.user.role !== 'USER') api<AnalyticsOverview>('/api/v1/analytics/overview').then(setAnalytics) }, [session.user.role])
  if (report === undefined) return <Spinner />
  const now = new Date(); const greeting = now.getHours() < 12 ? '좋은 아침입니다' : now.getHours() < 18 ? '좋은 오후입니다' : '수고 많으셨습니다'
  return <><PageHeader title={`${greeting}, ${session.user.displayName}님`} description="이번 주 업무 흐름과 팀 현황을 한눈에 확인하세요." />
    <div className="dashboard-grid">
      <Card className="hero-card"><div className="week-label">CURRENT WEEK</div><h3>{weekLabel(report?.weekStart)}</h3>{report ? <><div className="status-line"><StatusBadge status={report.status}/><span>업무 항목 {report.items.length}개</span></div><p className="summary-preview">{report.summary || '주간 요약이 아직 없습니다.'}</p><Button onClick={() => navigate('current')}>보고서 열기</Button></> : <><p>아직 이번 주 보고서를 시작하지 않았습니다.</p><Button onClick={() => navigate('current')}>이번 주 보고서 작성</Button></>}</Card>
      <Card title="나의 업무 항목"><div className="mini-stats"><div><strong>{report?.items.length ?? 0}</strong><span>전체 업무</span></div><div><strong>{report?.items.filter(i => i.issue.trim()).length ?? 0}</strong><span>등록 이슈</span></div><div><strong>{report?.items.length ? Math.round(report.items.reduce((sum, i) => sum + i.progress, 0) / report.items.length) : 0}%</strong><span>평균 진척도</span></div></div></Card>
      {analytics && <Card title="팀 제출 현황" action={<button className="text-button" onClick={() => navigate('team')}>자세히 →</button>}><div className="progress-ring-row"><div className="ring" style={{ '--percent': `${analytics.submissionRate * 3.6}deg` } as React.CSSProperties}><strong>{Math.round(analytics.submissionRate)}%</strong></div><div><b>{analytics.submittedUsers} / {analytics.totalUsers}명 제출</b><p>현재 주차 기준 제출·확정 보고서</p></div></div></Card>}
      {changes && changes.reported > 0 && <Card title="지난주 대비 변화" action={<span className="muted">{changes.previousWeek} → {changes.week}</span>}>
        <p className="muted">보고된 {changes.reported}건 중 {changes.changed}건이 지난주와 달라졌습니다. 그대로 진행 중인 업무는 세지 않고 보여 주지도 않습니다.</p>
        <ul className="change-counts">{changes.groups.map(group => <li key={group.kind} className={group.count > 0 ? `on ${group.kind.toLowerCase()}` : ''}>
          <strong>{group.count}</strong><span>{group.title}</span>
        </li>)}</ul>
      </Card>}
      <Card title="이번 주 이슈">{report && report.items.some(i => i.issue.trim()) ? <ul className="issue-list">{report.items.filter(i => i.issue.trim()).map((item, index) => <li key={index}><span>{item.title}</span><p>{item.issue}</p></li>)}</ul> : <Empty>등록된 이슈가 없습니다.</Empty>}</Card>
    </div>
  </>
}

function weekLabel(value?: string) { const date = value ? new Date(`${value}T00:00:00`) : new Date(); const end = new Date(date); end.setDate(date.getDate() + 6); return `${date.getMonth() + 1}월 ${date.getDate()}일 — ${end.getMonth() + 1}월 ${end.getDate()}일` }
