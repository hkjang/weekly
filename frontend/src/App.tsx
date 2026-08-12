import { useEffect, useState } from 'react'
import { api, post } from './api'
import { Button, Spinner, Toast } from './components'
import type { Providers, SessionInfo } from './types'
import DashboardPage from './pages/DashboardPage'
import ReportEditorPage from './pages/ReportEditorPage'
import ReportsPage from './pages/ReportsPage'
import TeamPage from './pages/TeamPage'
import AnalyticsPage from './pages/AnalyticsPage'
import ProfilePage from './pages/ProfilePage'
import AdminPage from './pages/AdminPage'
import ImportPage from './pages/ImportPage'
import RollupPage from './pages/RollupPage'

type Page = 'dashboard' | 'current' | 'history' | 'rollup' | 'import' | 'team' | 'analytics' | 'profile' | 'admin'
const pages: Page[] = ['dashboard', 'current', 'history', 'rollup', 'import', 'team', 'analytics', 'profile', 'admin']

function pageFromLocation(): Page | undefined {
  const value = window.location.hash.replace(/^#\/?/, '')
  return pages.includes(value as Page) ? value as Page : undefined
}

function replacePageLocation(page: Page) {
  window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}#/${page}`)
}

export default function App() {
  const [providers, setProviders] = useState<Providers>()
  const [session, setSession] = useState<SessionInfo>()
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState<Page>(() => pageFromLocation() ?? 'dashboard')
  const [profileOpen, setProfileOpen] = useState(false)
  const [toast, setToast] = useState<{ message: string; kind: 'success' | 'error' }>()

  const notify = (message: string, kind: 'success' | 'error' = 'success') => setToast({ message, kind })
  const refreshSession = async () => { const value = await api<SessionInfo>('/api/v1/me'); setSession(value) }
  useEffect(() => {
    Promise.all([api<Providers>('/api/v1/auth/providers'), api<SessionInfo>('/api/v1/me').catch(() => undefined)])
      .then(([p, s]) => { setProviders(p); setSession(s) }).finally(() => setLoading(false))
  }, [])
  useEffect(() => {
    if (!pageFromLocation()) replacePageLocation('dashboard')
    const syncPage = () => setPage(pageFromLocation() ?? 'dashboard')
    window.addEventListener('hashchange', syncPage)
    return () => window.removeEventListener('hashchange', syncPage)
  }, [])
  useEffect(() => {
    if (!session) return
    const teamOnly = page === 'team' || page === 'analytics'
    const denied = (teamOnly && session.user.role === 'USER') || (page === 'admin' && session.user.role !== 'ADMIN')
    if (denied) {
      replacePageLocation('dashboard')
      setPage('dashboard')
    }
  }, [page, session])

  if (loading) return <div className="splash"><div className="brand-mark">W</div><Spinner /></div>
  if (!providers) return <div className="splash"><p>서비스 정보를 불러올 수 없습니다.</p></div>
  if (!session) return <Login providers={providers} onLogin={async () => { await refreshSession() }} notify={notify} />

  const canTeam = session.user.role !== 'USER'
  const isAdmin = session.user.role === 'ADMIN'
  const navigate = (next: Page) => { if (page !== next) window.location.hash = `/${next}`; setPage(next); setProfileOpen(false) }
  const logout = async () => { await post('/api/v1/auth/logout'); replacePageLocation('dashboard'); setPage('dashboard'); setSession(undefined); setProfileOpen(false) }

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark small">W</span><div><strong>{session.serviceName}</strong><small>Weekly reports</small></div></div>
      <nav>
        <span className="nav-label">개인</span>
        <Nav active={page === 'dashboard'} icon="⌂" onClick={() => navigate('dashboard')}>대시보드</Nav>
        <Nav active={page === 'current'} icon="✎" onClick={() => navigate('current')}>내 주간보고</Nav>
        <Nav active={page === 'history'} icon="◷" onClick={() => navigate('history')}>과거 보고</Nav>
        <Nav active={page === 'rollup'} icon="▤" onClick={() => navigate('rollup')}>기간 업무보고</Nav>
        <Nav active={page === 'import'} icon="⇧" onClick={() => navigate('import')}>PPTX 가져오기</Nav>
        {canTeam && <><span className="nav-label">조직</span><Nav active={page === 'team'} icon="♙" onClick={() => navigate('team')}>팀 주간보고</Nav><Nav active={page === 'analytics'} icon="▥" onClick={() => navigate('analytics')}>보고 분석</Nav></>}
        {isAdmin && <><span className="nav-label">관리</span><Nav active={page === 'admin'} icon="⚙" onClick={() => navigate('admin')}>관리자 설정</Nav></>}
      </nav>
      <div className="sidebar-foot">오프라인 운영 준비됨</div>
    </aside>
    <main className="main-shell">
      <header className="topbar">
        <div className="mobile-title">{session.serviceName}</div>
        <div className="profile-wrap">
          <button className="profile-button" onClick={() => setProfileOpen(!profileOpen)}><span className="avatar">{session.user.displayName.slice(0, 1)}</span><span><strong>{session.user.displayName}</strong><small>{roleName(session.user.role)}</small></span><b>⌄</b></button>
          {profileOpen && <div className="profile-menu"><button onClick={() => navigate('profile')}>개인 설정 · API 키</button><div className="version-row"><span>서비스 버전</span><strong>v{session.build.version}</strong><small>{session.build.commit.slice(0, 8)}</small></div><button onClick={logout}>로그아웃</button></div>}
        </div>
      </header>
      {session.notice && <div className="notice">{session.notice}</div>}
      <div className="page-content">
        {page === 'dashboard' && <DashboardPage session={session} navigate={navigate} />}
        {page === 'current' && <ReportEditorPage workflowEnabled={session.workflowEnabled} aiEnabled={session.aiEnabled} notify={notify} />}
        {page === 'history' && <ReportsPage currentWeekStart={session.currentWeekStart} notify={notify} />}
        {page === 'rollup' && <RollupPage session={session} notify={notify} />}
        {page === 'import' && <ImportPage aiEnabled={session.aiEnabled} currentWeekStart={session.currentWeekStart} notify={notify} />}
        {page === 'team' && canTeam && <TeamPage workflowEnabled={session.workflowEnabled} currentUserId={session.user.id} notify={notify} />}
        {page === 'analytics' && canTeam && <AnalyticsPage />}
        {page === 'profile' && <ProfilePage session={session} notify={notify} refreshSession={refreshSession} />}
        {page === 'admin' && isAdmin && <AdminPage notify={notify} onSettingsChanged={refreshSession} />}
      </div>
    </main>
    {toast && <Toast {...toast} onClose={() => setToast(undefined)} />}
  </div>
}

function Nav({ active, icon, children, onClick }: { active: boolean; icon: string; children: string; onClick: () => void }) { return <button className={active ? 'active' : ''} onClick={onClick}><i>{icon}</i>{children}</button> }
function roleName(role: string) { return ({ USER: '사용자', TEAM_LEADER: '팀장', ORG_MANAGER: '조직장', ADMIN: '관리자' } as Record<string, string>)[role] ?? role }

function Login({ providers, onLogin, notify }: { providers: Providers; onLogin: () => Promise<void>; notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [username, setUsername] = useState(''); const [password, setPassword] = useState(''); const [busy, setBusy] = useState(false)
  const submit = async (event: React.FormEvent) => { event.preventDefault(); setBusy(true); try { await post('/api/v1/auth/login', { username, password }); await onLogin() } catch (error) { notify(error instanceof Error ? error.message : '로그인할 수 없습니다.', 'error') } finally { setBusy(false) } }
  return <div className="login-page"><div className="login-panel"><div className="login-brand"><span className="brand-mark">W</span><div><h1>{providers.name}</h1><p>한 주의 성과를 선명하게 기록하세요.</p></div></div>{providers.notice && <div className="login-notice">{providers.notice}</div>}
    {providers.local && <form onSubmit={submit}><label>아이디<input autoFocus autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required /></label><label>비밀번호<input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required /></label><Button disabled={busy} className="full">{busy ? '로그인 중…' : '로그인'}</Button></form>}
    {providers.local && providers.oidc && <div className="divider"><span>또는</span></div>}
    {providers.oidc && <a className="button secondary full sso" href="/api/v1/auth/oidc/start">Keycloak SSO로 로그인</a>}
    {!providers.local && !providers.oidc && <p className="login-error">활성화된 로그인 방식이 없습니다. 관리자에게 문의하세요.</p>}
    <footer><span>{providers.name} v{providers.build.version}</span><span>Commit {providers.build.commit.slice(0, 8)}</span></footer>
  </div><div className="login-art"><div className="orb one"/><div className="orb two"/><div className="week-grid">{['MON','TUE','WED','THU','FRI'].map((day, index) => <div key={day} style={{ '--i': index } as React.CSSProperties}><strong>{day}</strong><span/><span/><span/></div>)}</div></div></div>
}
