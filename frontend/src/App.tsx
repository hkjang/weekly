import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { errorText, api, post } from './api'
import { Button, Spinner, Toast } from './components'
import CommandPalette, { periodCommands } from './CommandPalette'
import type { Command } from './CommandPalette'
import { appHandlesClick, navigateTo, parseRoute, replaceRoute, routeHash } from './router'
import { confirmDiscard, hasUnsavedWork } from './unsavedGuard'
import { onSessionLost } from './session'
import { popLayer, pushLayer } from './layers'
import ErrorBoundary from './ErrorBoundary'
import type { PageName } from './router'
import type { Providers, SessionInfo } from './types'
import DashboardPage from './pages/DashboardPage'
import ReportEditorPage from './pages/ReportEditorPage'
import ReportsPage from './pages/ReportsPage'
import TeamPage from './pages/TeamPage'
import AnalyticsPage from './pages/AnalyticsPage'
import ProfilePage from './pages/ProfilePage'
import AdminPage from './pages/AdminPage'
import ImportPage from './pages/ImportPage'
import MeetingPage from './pages/MeetingPage'
import DigestPage from './pages/DigestPage'
import InsightsPage from './pages/InsightsPage'
import HandoverPage from './pages/HandoverPage'
import RollupPage from './pages/RollupPage'
import WorkItemsPage from './pages/WorkItemsPage'

type Page = PageName

// Single letter jumps after pressing `g`, the way keyboard driven tools do it.
const gotoKeys: Record<string, Page> = {
  d: 'dashboard', c: 'current', h: 'history', w: 'work', r: 'rollup',
  i: 'import', t: 'team', a: 'analytics', p: 'profile', s: 'admin',
}

/** typingInFormField keeps shortcuts from firing while the user writes a report. */
function typingInFormField(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null
  if (!element) return false
  const tag = element.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || element.isContentEditable
}

export default function App() {
  const [providers, setProviders] = useState<Providers>()
  const [session, setSession] = useState<SessionInfo>()
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState<Page>(() => parseRoute()?.page ?? 'dashboard')
  const [params, setParams] = useState<Record<string, string>>(() => parseRoute()?.params ?? {})
  const [profileOpen, setProfileOpen] = useState(false)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [toast, setToast] = useState<{ message: string; kind: 'success' | 'error'; id: number }>()
  const [sessionExpired, setSessionExpired] = useState(false)
  // On a phone the navigation is a strip that scrolls, so the screen you are on
  // has to be brought into view; otherwise the later entries look missing.
  const navRef = useRef<HTMLElement>(null)
  const [signedOut, setSignedOut] = useState(false)

  const notify = (message: string, kind: 'success' | 'error' = 'success') => setToast(previous => ({ message, kind, id: (previous?.id ?? 0) + 1 }))
  const refreshSession = async () => { const value = await api<SessionInfo>('/api/v1/me'); setSession(value) }
  useEffect(() => {
    Promise.all([api<Providers>('/api/v1/auth/providers'), api<SessionInfo>('/api/v1/me').catch(() => undefined)])
      .then(([p, s]) => { setProviders(p); setSession(s) }).finally(() => setLoading(false))
  }, [])
  // The API layer announces a 401; deciding what to do with it belongs here.
  const sessionRef = useRef<SessionInfo | undefined>(undefined)
  sessionRef.current = session
  useEffect(() => {
    onSessionLost(() => {
      // Nothing to lose before the first login, and the probe on start-up is a
      // 401 by design.
      if (!sessionRef.current) return
      // Never throw away typed work to show a login form. The editor is the one
      // screen that can be holding an hour of it, and the session cookie is
      // shared with any other tab, so signing in elsewhere makes this screen
      // work again with its content intact.
      if (hasUnsavedWork()) { setSessionExpired(true); return }
      setSessionExpired(false)
      setSession(undefined)
      // A banner on the login screen rather than a toast: the screen that fails
      // first also raises its own error toast, and whichever lands last wins.
      // The reason the user is looking at a login form should not be a race.
      setSignedOut(true)
    })
    return () => onSessionLost(null)
  }, [])
  useEffect(() => {
    if (!parseRoute()) replaceRoute('dashboard')
    // The hash is the source of truth, so the guard has to run here as well as
    // in go(): the back button, the palette and a hand-edited URL all arrive
    // through this event without passing the navigation helper.
    let current = window.location.hash
    let restoring = false
    const syncPage = () => {
      if (restoring) { restoring = false; return }
      if (!confirmDiscard()) {
        restoring = true
        window.location.hash = current
        return
      }
      current = window.location.hash
      const route = parseRoute()
      setPage(route?.page ?? 'dashboard')
      setParams(route?.params ?? {})
    }
    // Closing the tab is the one exit React cannot intercept; the browser's own
    // dialog is the only thing that can stop it.
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!hasUnsavedWork()) return
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('hashchange', syncPage)
    window.addEventListener('beforeunload', beforeUnload)
    return () => {
      window.removeEventListener('hashchange', syncPage)
      window.removeEventListener('beforeunload', beforeUnload)
    }
  }, [])
  useEffect(() => {
    navRef.current?.querySelector('button.active')?.scrollIntoView({ block: 'nearest', inline: 'center' })
  }, [page])

  // The palette is a layer like any other, so a dialog underneath it does not
  // also close when Escape dismisses the palette.
  useEffect(() => {
    if (!paletteOpen) return
    const id = pushLayer()
    return () => popLayer(id)
  }, [paletteOpen])
  useEffect(() => {
    if (!session) return
    const teamOnly = page === 'team' || page === 'analytics' || page === 'digest' || page === 'insights'
    const denied = (teamOnly && session.user.role === 'USER') || (page === 'admin' && session.user.role !== 'ADMIN')
    if (denied) {
      replaceRoute('dashboard')
      setPage('dashboard')
      setParams({})
    }
  }, [page, session])

  const go = useCallback((next: Page, nextParams?: Record<string, string>) => {
    if (routeHash(next, nextParams) !== window.location.hash && !confirmDiscard()) return
    setProfileOpen(false)
    navigateTo(next, nextParams)
    setPage(next)
    setParams(nextParams ?? {})
  }, [])

  // Ctrl+K opens the palette anywhere; `g` then a letter jumps straight to a
  // screen. Both stay silent while the caret is in a form field.
  useEffect(() => {
    if (!session) return
    let awaitingGoto = false
    let timer = 0
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen(current => !current)
        return
      }
      if (event.key === 'Escape' && paletteOpen) { setPaletteOpen(false); return }
      // Anything below is only for the page itself, never for an overlay.
      if (paletteOpen || event.ctrlKey || event.metaKey || event.altKey || typingInFormField(event.target)) return
      if (event.key === '/' || event.key === '?') { event.preventDefault(); setPaletteOpen(true); return }
      if (awaitingGoto) {
        window.clearTimeout(timer)
        awaitingGoto = false
        const target = gotoKeys[event.key.toLowerCase()]
        if (!target) return
        const allowed = target === 'admin' ? session.user.role === 'ADMIN'
          : (target === 'team' || target === 'analytics') ? session.user.role !== 'USER' : true
        if (allowed) { event.preventDefault(); go(target) }
        return
      }
      if (event.key.toLowerCase() === 'g') {
        awaitingGoto = true
        timer = window.setTimeout(() => { awaitingGoto = false }, 1200)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => { window.removeEventListener('keydown', onKeyDown); window.clearTimeout(timer) }
  }, [session, paletteOpen, go])

  if (loading) return <div className="splash"><div className="brand-mark">W</div><Spinner /></div>
  if (!providers) return <div className="splash"><p>서비스 정보를 불러올 수 없습니다.</p></div>
  // The toast lives in the shell below, which this branch never reaches, so it
  // is rendered here too. Without it the "your session expired" message is
  // created and then thrown away at the exact moment it is needed.
  if (!session) return <>
    <Login providers={providers} notify={notify}
      notice={signedOut ? '세션이 만료되어 로그아웃됐습니다. 다시 로그인해 주세요.' : undefined}
      onLogin={async () => { setSessionExpired(false); setSignedOut(false); await refreshSession() }} />
    {toast && <Toast key={toast.id} {...toast} onClose={() => setToast(undefined)} />}
  </>

  const canTeam = session.user.role !== 'USER'
  const isAdmin = session.user.role === 'ADMIN'
  const navigate = (next: Page) => go(next)
  const logout = async () => { if (!confirmDiscard()) return; setSignedOut(false); await post('/api/v1/auth/logout'); replaceRoute('dashboard'); setPage('dashboard'); setParams({}); setSession(undefined); setProfileOpen(false) }

  const screens: { page: Page; label: string; keywords: string[]; visible: boolean }[] = [
    { page: 'dashboard', label: '대시보드', keywords: ['dashboard', 'home', '홈'], visible: true },
    { page: 'current', label: '내 주간보고', keywords: ['current', 'weekly', '작성', '임시저장', '제출'], visible: true },
    { page: 'history', label: '과거 보고', keywords: ['history', 'past', '이전', '복제'], visible: true },
    { page: 'work', label: '업무 추적', keywords: ['work', 'workitem', '업무', '정체', '경과', 'aging'], visible: true },
    { page: 'meeting', label: '회의 모드', keywords: ['meeting', '회의', '안건', '발표', 'agenda', 'presentation'], visible: true },
    { page: 'handover', label: '인수인계', keywords: ['handover', '인수', '인계', '이관', '담당자 변경'], visible: true },
    { page: 'digest', label: '경영 요약', keywords: ['digest', '경영', '요약', 'executive', '핵심'], visible: canTeam },
    { page: 'insights', label: '업무 인사이트', keywords: ['insight', '인사이트', '중복', '유사', '협업', '반복'], visible: canTeam },
    { page: 'rollup', label: '기간 업무보고', keywords: ['rollup', 'period', '월간', '분기', '반기', '연간'], visible: true },
    { page: 'import', label: 'PPTX 가져오기', keywords: ['import', 'pptx', '업로드'], visible: true },
    { page: 'team', label: '팀 주간보고', keywords: ['team', '조직', '승인', '검토'], visible: canTeam },
    { page: 'analytics', label: '보고 분석', keywords: ['analytics', '분석', '제출률'], visible: canTeam },
    { page: 'profile', label: '개인 설정 · API 키', keywords: ['profile', 'api', 'key', '키'], visible: true },
    { page: 'admin', label: '관리자 설정', keywords: ['admin', 'settings', '설정'], visible: isAdmin },
  ]
  const paletteCommands: Command[] = [
    ...screens.filter(screen => screen.visible).map(screen => ({
      id: `screen:${screen.page}`,
      label: screen.label,
      hint: gotoHint(screen.page),
      group: '화면',
      keywords: screen.keywords,
      run: () => go(screen.page),
    })),
    ...periodCommands(new Date(), go),
    {
      id: 'action:logout', label: '로그아웃', group: '액션',
      keywords: ['logout', 'signout', '나가기'], run: () => { void logout() },
    },
  ]

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark small">W</span><div><strong>{session.serviceName}</strong><small>Weekly reports</small></div></div>
      <nav ref={navRef}>
        <span className="nav-label">개인</span>
        <Nav page='dashboard' active={page === 'dashboard'} icon="⌂" onClick={() => navigate('dashboard')}>대시보드</Nav>
        <Nav page='current' active={page === 'current'} icon="✎" onClick={() => navigate('current')}>내 주간보고</Nav>
        <Nav page='history' active={page === 'history'} icon="◷" onClick={() => navigate('history')}>과거 보고</Nav>
        <Nav page='work' active={page === 'work'} icon="◎" onClick={() => navigate('work')}>업무 추적</Nav>
        <Nav page='meeting' active={page === 'meeting'} icon="◍" onClick={() => navigate('meeting')}>회의 모드</Nav>
        <Nav page='handover' active={page === 'handover'} icon="⇄" onClick={() => navigate('handover')}>인수인계</Nav>
        <Nav page='rollup' active={page === 'rollup'} icon="▤" onClick={() => navigate('rollup')}>기간 업무보고</Nav>
        <Nav page='import' active={page === 'import'} icon="⇧" onClick={() => navigate('import')}>PPTX 가져오기</Nav>
        {canTeam && <><span className="nav-label">조직</span><Nav page='team' active={page === 'team'} icon="♙" onClick={() => navigate('team')}>팀 주간보고</Nav><Nav page='analytics' active={page === 'analytics'} icon="▥" onClick={() => navigate('analytics')}>보고 분석</Nav><Nav page='digest' active={page === 'digest'} icon="★" onClick={() => navigate('digest')}>경영 요약</Nav><Nav page='insights' active={page === 'insights'} icon="⌘" onClick={() => navigate('insights')}>업무 인사이트</Nav></>}
        {isAdmin && <><span className="nav-label">관리</span><Nav page='admin' active={page === 'admin'} icon="⚙" onClick={() => navigate('admin')}>관리자 설정</Nav></>}
      </nav>
      <div className="sidebar-foot">오프라인 운영 준비됨</div>
    </aside>
    <main className="main-shell">
      <header className="topbar">
        <div className="mobile-title">{session.serviceName}</div>
        <button className="palette-trigger" onClick={() => setPaletteOpen(true)} aria-label="빠른 이동 열기">
          <span aria-hidden="true">⌕</span><span className="palette-trigger-text">빠른 이동</span><kbd>{shortcutLabel()}</kbd>
        </button>
        <div className="profile-wrap">
          <button className="profile-button" onClick={() => setProfileOpen(!profileOpen)}><span className="avatar">{session.user.displayName.slice(0, 1)}</span><span><strong>{session.user.displayName}</strong><small>{roleName(session.user.role)}</small></span><b>⌄</b></button>
          {profileOpen && <div className="profile-menu"><button onClick={() => navigate('profile')}>개인 설정 · API 키</button><div className="version-row"><span>서비스 버전</span><strong>v{session.build.version}</strong><small>{session.build.commit.slice(0, 8)}</small></div><button onClick={logout}>로그아웃</button></div>}
        </div>
      </header>
      {session.notice && <div className="notice">{session.notice}</div>}
      {/* About this account rather than the service, and it stays until an
          administrator fixes it — every org-scoped screen is blank meanwhile. */}
      {session.accountNotice && <div className="notice account-notice" role="alert">{session.accountNotice}</div>}
      {sessionExpired && <div className="session-expired" role="alert">
        <div>
          <strong>세션이 만료되었습니다.</strong>
          <span>작성 중인 내용이 있어 이 화면을 그대로 두었습니다. 새 탭에서 다시 로그인한 뒤 이 화면으로 돌아와 저장하세요.</span>
        </div>
        <button className="button" onClick={() => window.open(window.location.pathname, '_blank', 'noopener')}>새 탭에서 로그인</button>
      </div>}
      <div className="page-content">
      <ErrorBoundary onReset={() => go('dashboard')}>
        {page === 'dashboard' && <DashboardPage session={session} navigate={navigate} />}
        {page === 'current' && <ReportEditorPage workflowEnabled={session.workflowEnabled} aiEnabled={session.aiEnabled} notify={notify} />}
        {page === 'history' && <ReportsPage currentWeekStart={session.currentWeekStart} openReportId={Number(params.report) || undefined} notify={notify} />}
        {page === 'work' && <WorkItemsPage session={session} notify={notify} />}
        {page === 'rollup' && <RollupPage session={session} route={params} notify={notify} />}
        {page === 'meeting' && <MeetingPage session={session} notify={notify} />}
        {page === 'handover' && <HandoverPage session={session} notify={notify} />}
        {page === 'digest' && canTeam && <DigestPage notify={notify} navigate={navigate} />}
        {page === 'insights' && canTeam && <InsightsPage notify={notify} />}
        {page === 'import' && <ImportPage aiEnabled={session.aiEnabled} currentWeekStart={session.currentWeekStart} notify={notify} />}
        {page === 'team' && canTeam && <TeamPage workflowEnabled={session.workflowEnabled} currentUserId={session.user.id} notify={notify} />}
        {page === 'analytics' && canTeam && <AnalyticsPage />}
        {page === 'profile' && <ProfilePage session={session} notify={notify} refreshSession={refreshSession} />}
        {page === 'admin' && isAdmin && <AdminPage notify={notify} onSettingsChanged={refreshSession} />}
      </ErrorBoundary>
      </div>
    </main>
    <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} session={session} go={go} commands={paletteCommands} />
    {toast && <Toast {...toast} onClose={() => setToast(undefined)} />}
  </div>
}

function gotoHint(page: Page): string {
  const key = Object.entries(gotoKeys).find(([, value]) => value === page)?.[0]
  return key ? `g ${key}` : ''
}

function shortcutLabel(): string {
  const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  return mac ? '⌘K' : 'Ctrl K'
}

/**
 * A navigation item is a real link that this app happens to handle itself.
 *
 * It was a button, because leaving a page with unsaved edits has to ask first
 * and a plain anchor would navigate before anything could ask. The cost was
 * everything a link gives you for free: middle click, ctrl+click, "새 탭에서
 * 열기", "링크 주소 복사". In a tool people keep open all day, comparing your
 * own report against the team's in a second tab is an ordinary thing to want,
 * and there was no way to do it.
 *
 * An anchor with an intercepted plain click keeps both. A modified click is
 * left alone: it opens a new tab, and nothing is being left behind in this one.
 */
function Nav({ active, icon, children, page, onClick }: { active: boolean; icon: string; children: string; page: Page; onClick: () => void }) {
  return <a
    href={routeHash(page)}
    className={active ? 'active' : ''}
    onClick={event => {
      if (!appHandlesClick(event)) return
      event.preventDefault()
      onClick()
    }}
  ><i>{icon}</i>{children}</a>
}
function roleName(role: string) { return ({ USER: '사용자', TEAM_LEADER: '팀장', ORG_MANAGER: '조직장', ADMIN: '관리자' } as Record<string, string>)[role] ?? role }

function Login({ providers, onLogin, notify, notice }: { providers: Providers; onLogin: () => Promise<void>; notify: (message: string, kind?: 'success' | 'error') => void; notice?: string }) {
  const [username, setUsername] = useState(''); const [password, setPassword] = useState(''); const [busy, setBusy] = useState(false)
  const submit = async (event: React.FormEvent) => { event.preventDefault(); setBusy(true); try { await post('/api/v1/auth/login', { username, password }); await onLogin() } catch (error) { notify(errorText(error, '로그인할 수 없습니다.'), 'error') } finally { setBusy(false) } }
  return <div className="login-page"><div className="login-panel"><div className="login-brand"><span className="brand-mark">W</span><div><h1>{providers.name}</h1><p>한 주의 성과를 선명하게 기록하세요.</p></div></div>{notice && <div className="login-expired" role="alert">{notice}</div>}{providers.notice && <div className="login-notice">{providers.notice}</div>}
    {providers.local && <form onSubmit={submit}><label>아이디<input autoFocus autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required /></label><label>비밀번호<input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required /></label><Button disabled={busy} className="full">{busy ? '로그인 중…' : '로그인'}</Button></form>}
    {providers.local && providers.oidc && <div className="divider"><span>또는</span></div>}
    {providers.oidc && <a className="button secondary full sso" href="/api/v1/auth/oidc/start">Keycloak SSO로 로그인</a>}
    {!providers.local && !providers.oidc && <p className="login-error">활성화된 로그인 방식이 없습니다. 관리자에게 문의하세요.</p>}
    <footer><span>{providers.name} v{providers.build.version}</span><span>Commit {providers.build.commit.slice(0, 8)}</span></footer>
  </div><div className="login-art"><div className="orb one"/><div className="orb two"/><div className="week-grid">{['MON','TUE','WED','THU','FRI'].map((day, index) => <div key={day} style={{ '--i': index } as React.CSSProperties}><strong>{day}</strong><span/><span/><span/></div>)}</div></div></div>
}
