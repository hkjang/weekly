import { useEffect, useState } from 'react'
import { APIError, UPLOAD_TIMEOUT_MS, errorText, api, del, patch, post, put } from '../api'
import { Modal, Button, Card, Empty, PageHeader, Spinner, formatDate } from '../components'
import AdminAnalyticsTab from './AdminAnalyticsTab'
import type { AdminUser, AdminUserPage, ConfluenceMapping, ConfluenceSyncStatus, EmbeddingStatus, EncryptionStatus, Organization, Setting, MailHealth } from '../types'

const weekdays = [
  { value: 'MONDAY', label: '월요일' }, { value: 'TUESDAY', label: '화요일' },
  { value: 'WEDNESDAY', label: '수요일' }, { value: 'THURSDAY', label: '목요일' },
  { value: 'FRIDAY', label: '금요일' }, { value: 'SATURDAY', label: '토요일' },
  { value: 'SUNDAY', label: '일요일' },
]

type Tab = 'analytics' | 'settings' | 'confluence' | 'users' | 'organizations' | 'pptx' | 'audit'
interface TemplateInfo { source: string; originalName: string; sizeBytes: number; sha256: string; placeholders: string[]; uploadedAt?: string; inheritedFrom?: string }

export default function AdminPage({ notify, onSettingsChanged }: { notify: (message: string, kind?: 'success' | 'error') => void; onSettingsChanged: () => Promise<void> }) {
  const [tab, setTab] = useState<Tab>('settings')
  return <><PageHeader title="관리자 관리" description="서비스 운영 설정과 계정·조직·템플릿·감사 이력을 중앙에서 관리합니다."/><div className="tabs"><TabButton value="analytics" active={tab} set={setTab}>분석</TabButton><TabButton value="settings" active={tab} set={setTab}>서비스 설정</TabButton><TabButton value="confluence" active={tab} set={setTab}>Confluence 자동화</TabButton><TabButton value="users" active={tab} set={setTab}>사용자</TabButton><TabButton value="organizations" active={tab} set={setTab}>조직</TabButton><TabButton value="pptx" active={tab} set={setTab}>PPTX 템플릿</TabButton><TabButton value="audit" active={tab} set={setTab}>감사 로그</TabButton></div>
    {tab === 'analytics' && <AdminAnalyticsTab notify={notify}/>} {tab === 'settings' && <SettingsTab notify={notify} changed={onSettingsChanged}/>} {tab === 'confluence' && <ConfluenceTab notify={notify}/>} {tab === 'users' && <UsersTab notify={notify}/>} {tab === 'organizations' && <OrganizationsTab notify={notify}/>} {tab === 'pptx' && <PPTXTab notify={notify}/>} {tab === 'audit' && <AuditTab/>}
  </>
}

function TabButton({ value, active, set, children }: { value: Tab; active: Tab; set: (tab: Tab) => void; children: string }) { return <button className={value === active ? 'active' : ''} onClick={() => set(value)}>{children}</button> }

const labels: Record<string, string> = { 'service.name': '서비스 이름', 'service.notice': '서비스 공지', 'service.timezone': '서비스 시간대', 'workflow.enabled': '팀장 검토·승인 사용', 'workflow.week_start': '주차 시작 요일', 'workflow.deadline_days': '제출 마감 — 주차 시작 후 며칠째', 'workflow.deadline_hour': '제출 마감 — 그날 몇 시까지 (24=자정)', 'auth.local_enabled': '로컬 로그인 사용', 'auth.session_hours': '세션 유효시간(시간)', 'auth.max_login_attempts': '계정당 로그인 실패 허용 횟수 (0=제한 없음)', 'auth.max_login_attempts_per_ip': 'IP당 로그인 실패 허용 횟수 (0=제한 없음)', 'auth.lockout_minutes': '로그인 차단 시간(분)', 'oidc.enabled': 'Keycloak OIDC 사용', 'oidc.issuer_url': 'Issuer URL', 'oidc.client_id': 'Client ID', 'oidc.client_secret': 'Client Secret', 'oidc.redirect_url': 'Redirect URL (선택)', 'oidc.scopes': 'OIDC Scopes', 'oidc.username_claim': '사용자명 Claim', 'oidc.groups_claim': '그룹 Claim', 'oidc.admin_group': '관리자 그룹', 'oidc.auto_provision': '사용자 자동 등록', 'security.api_key_max_days': 'API 키 최대 유효일', 'analytics.retention_days': '분석 데이터 보관일', 'attachment.max_per_report': '보고서당 최대 캡처 수', 'attachment.max_file_mb': '캡처 파일당 최대 MB', 'search.similarity_threshold': '유사 검색 최소 점수(%)', 'search.semantic_threshold': '의미 검색 최소 점수(%)', 'ai.embedding_enabled': '의미 기반 검색 사용', 'ai.embedding_endpoint': 'Embeddings Endpoint', 'ai.embedding_model': '임베딩 모델', 'ai.enabled': 'AI 작성·PPTX 분석 사용', 'ai.endpoint': 'Chat Completions Endpoint', 'ai.api_key': 'AI API Key', 'ai.model': 'AI 모델', 'ai.timeout_seconds': 'AI 요청 제한시간(초)', 'ai.max_input_chars': 'AI 최대 입력 글자수', 'import.max_files': '한 번에 업로드할 PPTX 수', 'import.max_file_mb': 'PPTX 파일당 최대 MB', 'import.retention_days': 'Import 원본 보관일', 'confluence.enabled': 'Confluence 자동 수집 사용', 'confluence.base_url': 'Confluence Base URL', 'confluence.auth_mode': '인증 방식', 'confluence.username': 'Service Account', 'confluence.password': 'Service Account 비밀번호', 'confluence.include_spaces': '포함 Space (쉼표 구분)', 'confluence.exclude_spaces': '제외 Space (쉼표 구분)', 'confluence.sync_interval_minutes': '자동 수집 주기(분)', 'confluence.ai_enabled': 'Confluence AI 분류·요약 사용', 'confluence.minimum_candidate_score': '자동 후보 최소 점수', 'confluence.ai_review_min_score': 'AI 검토 최소 점수', 'confluence.analyze_body': '후보 문서 본문 분석', 'confluence.lookback_days': '첫 수집 조회 기간(일)', 'confluence.batch_size': 'API 페이지 크기', 'confluence.include_blogs': 'Blog Post 포함', 'confluence.auto_map_email_localpart': '이메일 @ 앞부분 자동 매핑', 'confluence.auto_map_username': '로그인 아이디 자동 매핑', 'confluence.work_keywords': '업무성 키워드', 'confluence.personal_space_prefixes': '개인 Space 접두사', 'confluence.score_project_space': '포함 Space 점수', 'confluence.score_creator': '본인 작성 점수', 'confluence.score_modifier': '본인 최종 수정 점수', 'confluence.score_work_keyword': '업무 키워드 점수', 'confluence.score_meeting': '회의 문서 점수', 'confluence.score_notice': '공지 문서 점수', 'confluence.score_leave': '휴가 문서 점수', 'confluence.score_personal_space': '개인 Space 점수', 'mail.enabled': '주간보고 메일 발송 사용', 'mail.host': 'SMTP 호스트', 'mail.port': 'SMTP 포트', 'mail.security': '연결 보안', 'mail.username': 'SMTP 계정 (선택)', 'mail.password': 'SMTP 비밀번호', 'mail.from': '보내는 주소', 'mail.from_name': '보내는 사람 이름', 'mail.timeout_seconds': '발송 제한시간(초)', 'mail.max_attempts': '재시도 횟수' }

function SettingsTab({ notify, changed }: { notify: (message: string, kind?: 'success' | 'error') => void; changed: () => Promise<void> }) {
  const [settings, setSettings] = useState<Setting[]>()
  const [values, setValues] = useState<Record<string, string>>({})
  const [embedding, setEmbedding] = useState<EmbeddingStatus>()
  // Where the secret encryption key lives decides whether a lost state volume
  // costs the secret settings. The README tells operators this matters; until
  // now the only place that said which mode a deployment was in was one line of
  // the boot log, gone by the time anybody asks.
  const [encryption, setEncryption] = useState<EncryptionStatus>()
  const load = () => api<Setting[]>('/api/v1/admin/settings').then(data => { setSettings(data); setValues(Object.fromEntries(data.map(item => [item.key, item.value ?? '']))) })
  // Coverage is what tells an operator whether semantic search can answer at
  // all, so it is loaded next to the settings that control it.
  const loadEmbedding = () => api<EmbeddingStatus>('/api/v1/admin/embeddings').then(setEmbedding).catch(() => setEmbedding(undefined))
  const loadEncryption = () => api<EncryptionStatus>('/api/v1/admin/encryption').then(setEncryption).catch(() => setEncryption(undefined))
  useEffect(() => { load(); loadEmbedding(); loadEncryption() }, [])
  const rebuildEmbeddings = async () => { try { const result = await api<{ embedded: number, remaining: number }>('/api/v1/admin/embeddings/rebuild',
      // Up to 6,400 items against a gateway with its own per-request timeout;
      // the default bound is for requests that should be instant.
      { method: 'POST', body: '{}', signal: AbortSignal.timeout(UPLOAD_TIMEOUT_MS) }); await loadEmbedding(); notify(result.remaining > 0 ? `임베딩 ${result.embedded.toLocaleString()}건을 생성했습니다. ${result.remaining.toLocaleString()}건이 남았고 백그라운드 작업자가 이어서 처리합니다.` : `임베딩 ${result.embedded.toLocaleString()}건을 생성했습니다. 남은 항목이 없습니다.`) } catch (error) { notify(errorText(error, '임베딩을 생성할 수 없습니다.'), 'error') } }
  // Saving a blank secret means "keep what is there", so a masked form cannot
  // wipe a password by being submitted. Removing one therefore needs its own
  // verb: an integration switched off, or a secret this deployment's key can no
  // longer read, otherwise stayed in the table with nothing to be done about it
  // except typing a password nobody wants stored.
  const clearSecret = async (key: string) => {
    if (!confirm(`${labels[key] ?? key} 값을 지우시겠습니까? 해당 기능은 값을 다시 입력할 때까지 동작하지 않습니다.`)) return
    try {
      await del(`/api/v1/admin/settings/${encodeURIComponent(key)}`)
      setValues(current => ({ ...current, [key]: '' }))
      // The row and the card read the same fact from two endpoints. Reloading
      // only the settings left the warning above still naming a key that was
      // no longer there, which is the screen disagreeing with itself.
      await Promise.all([load(), loadEncryption()])
      notify(`${labels[key] ?? key} 값을 지웠습니다.`)
    } catch (error) {
      notify(errorText(error, '설정을 지울 수 없습니다.'), 'error')
    }
  }
  // A change the server refuses until its consequences are accepted comes back
  // as 409 with the explanation. Showing that text and asking is better than a
  // warning nobody reads next to a field they are not touching.
  const save = async (confirmed: string[] = []) => {
    try {
      await put('/api/v1/admin/settings', { settings: values, confirmed })
      await load(); await changed(); notify('서비스 설정을 저장했습니다.')
    } catch (error) {
      if (error instanceof APIError && error.code === 'WEEK_START_NEEDS_CONFIRMATION') {
        if (confirm(`${error.message}\n\n계속하시겠습니까?`)) { await save(['workflow.week_start']); return }
        notify('주차 시작 요일을 바꾸지 않았습니다.')
        return
      }
      notify(errorText(error, '설정을 저장할 수 없습니다.'), 'error')
    }
  }
  const testOIDC = async () => { try { await post('/api/v1/admin/settings/oidc/test'); notify('OIDC Discovery 연결에 성공했습니다.') } catch (error) { notify(errorText(error, 'OIDC 연결에 실패했습니다.'), 'error') } }
  const testAI = async () => { try { const result = await post<{ model: string; items: number }>('/api/v1/admin/settings/ai/test'); notify(`${result.model} AI 연결과 Structured Output 검증에 성공했습니다.`) } catch (error) { notify(errorText(error, 'AI 연결에 실패했습니다.'), 'error') } }
  // Sent to the administrator running the test, not to an address they type:
  // a test that can be pointed anywhere is a way to send mail from this server
  // to a stranger, and the question here is only whether the relay works.
  const [mailHealth, setMailHealth] = useState<MailHealth>()
  // Read once with the settings. It is a figure to glance at, not a live feed.
  useEffect(() => { api<MailHealth>('/api/v1/admin/mail/health').then(setMailHealth).catch(() => setMailHealth(undefined)) }, [])
  const testMail = async () => { try { const result = await post<{ to: string }>('/api/v1/admin/settings/mail/test'); notify(`${result.to} 로 시험 메일을 보냈습니다.`) } catch (error) { notify(errorText(error, '메일 발송에 실패했습니다.'), 'error') } }
  const testConfluence = async () => { try { await post('/api/v1/admin/settings/confluence/test'); notify('Confluence 6.9.1 REST API 연결에 성공했습니다.') } catch (error) { notify(errorText(error, 'Confluence 연결에 실패했습니다.'), 'error') } }
  if (!settings) return <Spinner/>
  const groups = [{ title: '일반 · 워크플로', keys: ['service.name','service.notice','service.timezone','workflow.enabled','workflow.week_start','workflow.deadline_days','workflow.deadline_hour'] }, { title: '인증 · Keycloak OIDC', keys: ['auth.local_enabled','auth.session_hours','auth.max_login_attempts','auth.lockout_minutes','auth.max_login_attempts_per_ip','oidc.enabled','oidc.issuer_url','oidc.client_id','oidc.client_secret','oidc.redirect_url','oidc.scopes','oidc.username_claim','oidc.groups_claim','oidc.admin_group','oidc.auto_provision'] }, { title: 'AI Gateway · 과거 자료 Import', keys: ['ai.enabled','ai.endpoint','ai.api_key','ai.model','ai.timeout_seconds','ai.max_input_chars','import.max_files','import.max_file_mb','import.retention_days'] }, { title: 'Confluence 6.9.1 자동화', keys: ['confluence.enabled','confluence.base_url','confluence.auth_mode','confluence.username','confluence.password','confluence.include_spaces','confluence.exclude_spaces','confluence.sync_interval_minutes','confluence.ai_enabled','confluence.minimum_candidate_score','confluence.ai_review_min_score','confluence.analyze_body','confluence.lookback_days','confluence.batch_size','confluence.include_blogs','confluence.auto_map_email_localpart','confluence.auto_map_username','confluence.work_keywords','confluence.personal_space_prefixes','confluence.score_project_space','confluence.score_creator','confluence.score_modifier','confluence.score_work_keyword','confluence.score_meeting','confluence.score_notice','confluence.score_leave','confluence.score_personal_space'] }, { title: '주간보고 메일 발송', keys: ['mail.enabled','mail.host','mail.port','mail.security','mail.username','mail.password','mail.from','mail.from_name','mail.timeout_seconds','mail.max_attempts'] }, { title: '화면 캡처 첨부', keys: ['attachment.max_per_report','attachment.max_file_mb'] }, { title: '보안 · 분석', keys: ['security.api_key_max_days','analytics.retention_days'] }, { title: '검색', keys: ['search.similarity_threshold','ai.embedding_enabled','ai.embedding_endpoint','ai.embedding_model','search.semantic_threshold'] }]
  const booleanKeys = ['workflow.enabled','auth.local_enabled','oidc.enabled','oidc.auto_provision','ai.enabled','confluence.enabled','confluence.ai_enabled','confluence.analyze_body','confluence.include_blogs','confluence.auto_map_email_localpart','confluence.auto_map_username','ai.embedding_enabled']
  return <div className="settings-stack">
    {/* 이미 일어난 일이 먼저입니다. 아래 두 문장은 키를 잃으면 무슨 일이
        벌어지는지에 대한 경고인데, 그 일이 이미 벌어진 배포에도 그대로
        걸려 있었습니다. */}
    {encryption && encryption.unreadable && encryption.unreadable.length > 0 && <p className="capture-warning">
      저장된 비밀 설정 <strong>{encryption.unreadable.length}개</strong>를 지금 쓰는 암호화 키로 읽을 수 없습니다 — {encryption.unreadable.map((key, index) => <span key={key}>{index > 0 && ', '}<code>{key}</code></span>)}.
      키가 바뀌었거나 <code>{encryption.stateDirectory}</code> 볼륨이 예전 것이 아닙니다. 예전 키를 <code>WEEKLY_ENCRYPTION_KEY</code>로 되돌리거나, 각 설정을 다시 입력해 저장하십시오. 그때까지 해당 기능은 동작하지 않습니다.</p>}
    {encryption && (encryption.recoverable
      ? <p className="setting-help">비밀 설정 암호화 키: {encryption.keySource === 'environment'
          ? `WEEKLY_ENCRYPTION_KEY 환경변수. ${encryption.stateDirectory} 볼륨을 잃어도 저장된 비밀 설정 ${encryption.storedSecrets}개를 복구할 수 있습니다.`
          : `${encryption.stateDirectory} 볼륨의 instance.key. 아직 저장된 비밀 설정이 없어 지금은 잃을 것이 없습니다.`}</p>
      : <p className="capture-warning">비밀 설정 암호화 키가 <code>{encryption.stateDirectory}</code> 볼륨 안에만 있습니다. 이 볼륨을 잃으면 저장된 비밀 설정 <strong>{encryption.storedSecrets}개</strong>를 복구할 수 없습니다. <code>WEEKLY_ENCRYPTION_KEY</code>를 지정하고 재기동하면 키가 볼륨과 분리됩니다.</p>)}
    {groups.map(group => <Card key={group.title} title={group.title}>{group.title === '화면 캡처 첨부' && <p className="setting-help">PPTX 내보내기는 첨부 이미지를 만들어지는 파일에 담습니다. 실측하면 요청 하나가 쓰는 메모리는 <strong>첨부 총량의 약 3.5배</strong>입니다 — 캡처 20장 44.9MB를 내보낼 때 컨테이너가 78MiB에서 235MiB로 올라갔습니다. 이미지는 하나씩 읽어 담지만, 완성된 파일 자체를 메모리에 들고 응답하기 때문입니다. 따라서 상한은 <code>보고서당 최대 캡처 수 × 캡처 파일당 최대 MB × 3.5</code>이고, 동시에 내보내는 사람 수만큼 곱해집니다. 기본값 20장 × 10MB 라면 요청 하나가 <strong>약 700MB</strong>입니다. 컨테이너 메모리 한도를 이 값으로 잡으세요.</p>}{group.title.startsWith('일반') && <p className="setting-help">제출 마감은 <strong>주차 시작일 기준</strong>입니다. 기본값 7일 24시는 주차 시작일로부터 7일째 되는 날 자정, 즉 다음 주 첫날 0시입니다. 금요일 15시가 마감이고 주차가 월요일에 시작한다면 4일 15시로 설정하세요. 판정은 서비스 시간대를 사용합니다.</p>}{group.title.startsWith('인증') && <p className="setting-help">로그인 실패가 허용 횟수에 이르면 <strong>차단 시간</strong> 동안 그 계정의 로그인을 거부합니다. 실패 횟수에 따라 응답도 점점 느려집니다. <strong>IP당 제한은 기본으로 꺼져 있습니다.</strong> 사무실 NAT이나 Reverse Proxy 뒤에서는 여러 사람이 같은 주소로 보이므로, 남의 오타 때문에 한 층이 통째로 잠길 수 있습니다. 클라이언트 주소가 실제로 구분되는 망에서만 켜십시오.</p>}{group.title.startsWith('AI Gateway') && <p className="setting-help">OpenAI 호환 <code>/v1/chat/completions</code> Endpoint와 Structured Output(JSON Schema)을 지원하는 모델을 지정하세요. 사내 AI Gateway는 API Key를 비워 둘 수 있습니다.</p>}{group.title === '주간보고 메일 발송' && <p className="setting-help">주간보고를 <strong>제출할 때</strong> 작성자가 개인 설정에 저장한 주소로 발송합니다. 발송은 제출 트랜잭션 밖에서 이루어지므로 <strong>릴레이가 죽어 있어도 제출은 실패하지 않습니다</strong> — 밀린 발송은 큐에 남아 다시 시도되고, 작성자는 개인 설정에서 그 상태를 봅니다. 사내 릴레이가 포트 25에 인증 없이 열려 있는 경우가 흔하므로 보안 <strong>없음(평문)</strong>이 기본값입니다. 다만 <strong>계정을 쓰려면 STARTTLS나 TLS가 필요합니다</strong> — 평문 연결에는 비밀번호를 실어 보내지 않습니다.</p>}{group.title === '검색' && <p className="setting-help">유사 검색은 PostgreSQL <code>pg_trgm</code>, 의미 검색은 <code>pgvector</code>와 OpenAI 호환 <code>/v1/embeddings</code> Endpoint가 필요합니다. 확장이 없으면 해당 단계는 자동으로 비활성화되고 기존 검색이 그대로 동작합니다. 코사인 점수 범위는 임베딩 모델마다 다르므로 <strong>의미 검색 최소 점수</strong>는 아래 임베딩 현황을 보며 조정하세요.</p>}{group.title.startsWith('Confluence') && <p className="setting-help">Confluence Server 6.9.1은 PAT 대신 연동 전용 Basic Auth 계정을 사용합니다. 입력한 비밀번호와 AI API Key는 암호화되어 저장되며 브라우저로 다시 전달되지 않습니다.</p>}{group.keys.map(key => { const setting = settings.find(s => s.key === key); if (!setting) return null; const isBoolean = booleanKeys.includes(key); const secretUnavailable = setting.secret && setting.configured && !setting.available; return <label className={isBoolean ? 'toggle-row' : 'setting-row'} key={key}><span><strong>{labels[key] ?? key}</strong>{setting.secret && setting.configured && <small className={secretUnavailable ? 'secret-unavailable' : ''}>{secretUnavailable ? '복호화할 수 없음 · 다시 입력 필요' : '현재 비밀값이 안전하게 설정됨'}{' '}<button type="button" className="text-button" onClick={() => clearSecret(key)}>지우기</button></small>}</span>{isBoolean ? <input type="checkbox" checked={values[key] === 'true'} onChange={e => setValues({ ...values, [key]: String(e.target.checked) })}/> : key === 'workflow.week_start' ? <select value={values[key]} onChange={e => setValues({ ...values, [key]: e.target.value })}>{weekdays.map(day => <option key={day.value} value={day.value}>{day.label}</option>)}</select> : key === 'confluence.auth_mode' ? <select value={values[key]} onChange={e => setValues({ ...values, [key]: e.target.value })}><option value="BASIC">Basic Auth</option><option value="NONE">Reverse Proxy 인증</option></select> : key === 'mail.security' ? <select value={values[key]} onChange={e => setValues({ ...values, [key]: e.target.value })}><option value="NONE">없음 (평문)</option><option value="STARTTLS">STARTTLS</option><option value="TLS">TLS</option></select> : <input type={setting.secret ? 'password' : 'text'} value={values[key]} placeholder={secretUnavailable ? '새 비밀값을 입력하세요' : setting.secret && setting.configured ? '변경할 때만 입력' : key === 'ai.endpoint' ? 'https://ai.internal/v1/chat/completions' : key === 'confluence.base_url' ? 'https://confluence.internal/confluence' : ''} onChange={e => setValues({ ...values, [key]: e.target.value })}/>}</label>})}{group.title.startsWith('AI Gateway') && <div className="inline-test"><Button variant="secondary" onClick={testAI}>AI Structured Output 연결 시험</Button></div>}{group.title.startsWith('Confluence') && <div className="inline-test"><Button variant="secondary" onClick={testConfluence}>Confluence REST 연결 시험</Button></div>}{group.title === '주간보고 메일 발송' && mailHealth && <p className="setting-help">최근 {mailHealth.days}일 · 발송 {mailHealth.sent.toLocaleString()}건 · 대기 {mailHealth.queued.toLocaleString()}건 · <strong className={mailHealth.failed > 0 ? 'danger-text' : undefined}>실패 {mailHealth.failed.toLocaleString()}건</strong> · 사용자 {mailHealth.writers.toLocaleString()}명{mailHealth.lastError && <><br/>마지막 실패 사유: <code>{mailHealth.lastError}</code></>}{mailHealth.sent === 0 && mailHealth.queued === 0 && mailHealth.failed === 0 && ' — 아직 발송한 주간보고가 없습니다. 사용자가 개인 설정에서 켜야 시작됩니다.'}</p>}{group.title === '주간보고 메일 발송' && <div className="inline-test"><span className="muted">시험 메일은 관리자 본인의 주소로 갑니다.</span><Button variant="secondary" onClick={testMail}>메일 발송 시험</Button></div>}{group.title === '검색' && <div className="inline-test">{embedding && <span className="muted">{embedding.vectorAvailable ? `pgvector 사용 가능 · 임베딩 ${embedding.embedded}/${embedding.items}건${embedding.stale > 0 ? ` · 수정 후 갱신 대기 ${embedding.stale}건` : ''}${embedding.reason ? ` · ${embedding.reason}` : ''}` : 'pgvector 미설치 · 의미 검색 비활성'}</span>}<Button variant="secondary" onClick={rebuildEmbeddings}>임베딩 다시 생성</Button></div>}</Card>)}<div className="admin-actions"><Button variant="secondary" onClick={testOIDC}>OIDC 연결 시험</Button><Button onClick={() => save()}>설정 저장</Button></div></div>
}

function ConfluenceTab({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [status, setStatus] = useState<ConfluenceSyncStatus>(); const [mappings, setMappings] = useState<ConfluenceMapping[]>(); const [values, setValues] = useState<Record<number,string>>({}); const [actives,setActives]=useState<Record<number,boolean>>({})
  const load = async () => { const [nextStatus,nextMappings] = await Promise.all([api<ConfluenceSyncStatus>('/api/v1/admin/confluence/sync/status'),api<ConfluenceMapping[]>('/api/v1/admin/confluence/users/mappings')]); setStatus(nextStatus);setMappings(nextMappings);setValues(Object.fromEntries(nextMappings.map(item=>[item.userId,item.externalUsername??item.suggestedUsername])));setActives(Object.fromEntries(nextMappings.map(item=>[item.userId,item.active??true]))) }
  useEffect(()=>{load();const timer=window.setInterval(()=>api<ConfluenceSyncStatus>('/api/v1/admin/confluence/sync/status').then(setStatus).catch(()=>undefined),10000);return()=>window.clearInterval(timer)},[])
  const sync=async()=>{try{await post('/api/v1/admin/confluence/sync');notify('Confluence 증분 동기화를 요청했습니다.');setTimeout(load,800)}catch(error){notify(error instanceof Error?error.message:'동기화를 요청할 수 없습니다.','error')}}
  const test=async()=>{try{await post('/api/v1/admin/settings/confluence/test');notify('Confluence REST 연결에 성공했습니다.')}catch(error){notify(error instanceof Error?error.message:'연결에 실패했습니다.','error')}}
  const save=async(item:ConfluenceMapping)=>{try{await put(`/api/v1/admin/confluence/users/${item.userId}/mapping`,{externalUsername:values[item.userId],active:actives[item.userId]});await load();notify(`${item.displayName} 사용자 매핑을 저장했습니다.`)}catch(error){notify(error instanceof Error?error.message:'매핑을 저장할 수 없습니다.','error')}}
  if(!status||!mappings)return <Spinner/>;return <><Card title="자동 수집 상태" action={<span className={`sync-chip ${status.status.toLowerCase()}`}>{status.status}</span>}><div className="sync-overview"><div><small>최근 성공</small><strong>{formatDate(status.lastSuccessAt)}</strong></div><div><small>조회 / 변경</small><strong>{status.pagesScanned} / {status.pagesChanged}</strong></div><div><small>새 초안</small><strong>{status.candidatesCreated}</strong></div><div><small>매핑 / 미매핑</small><strong>{status.mappedUsers} / {status.unmappedUsers}</strong></div></div>{status.unresolvedActors>0&&<div className="sync-unattributed">Weekly 계정에 연결되지 않은 Confluence 사용자 {status.unresolvedActors}명 때문에 페이지 {status.unattributedPages}개가 초안이 되지 못했습니다. 아래 진단에서 아이디를 확인해 매핑하세요.</div>}{status.errorMessage&&<div className="import-error">{status.errorMessage}</div>}<div className="admin-actions"><Button variant="secondary" onClick={test}>연결 시험</Button><Button onClick={sync} disabled={!status.enabled||status.status==='RUNNING'}>{status.status==='RUNNING'?'동기화 중…':'지금 증분 동기화'}</Button></div></Card><Card title="Weekly ↔ Confluence 사용자 매핑"><p className="muted"><b>명시 매핑 → 이메일 @ 앞부분 → 로그인 아이디</b> 순서로 자동 연결합니다. 예: <code>hkjang@koreacb.com → hkjang</code>. 자동 판정이 유일하지 않은 사용자만 직접 지정하세요.</p><div className="table-wrap"><table><thead><tr><th>Weekly 사용자</th><th>Keycloak 이메일</th><th>Confluence 아이디</th><th>활성</th><th>매핑 근거</th><th/></tr></thead><tbody>{mappings.map(item=><tr key={item.userId}><td><strong>{item.displayName}</strong><small className="cell-sub">{item.username}</small></td><td>{item.email||'-'}</td><td><input value={values[item.userId]??''} onChange={e=>setValues({...values,[item.userId]:e.target.value})}/></td><td><input type="checkbox" checked={actives[item.userId]??true} onChange={e=>setActives({...actives,[item.userId]:e.target.checked})}/></td><td>{item.mappingSource??`추천: ${item.suggestionSource}`}</td><td><Button variant="secondary" onClick={()=>save(item)}>저장</Button></td></tr>)}</tbody></table></div></Card>{status.recentErrors.length>0&&<Card title="최근 동기화 진단"><div className="table-wrap"><table><thead><tr><th>시간</th><th>단계</th><th>Page</th><th>HTTP</th><th>내용</th></tr></thead><tbody>{status.recentErrors.map(item=><tr key={item.id}><td>{formatDate(item.createdAt)}</td><td><code>{item.phase}</code></td><td>{item.pageId??'-'}</td><td>{item.statusCode??'-'}</td><td>{item.message}</td></tr>)}</tbody></table></div></Card>}</>
}

function UsersTab({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [users, setUsers] = useState<AdminUser[]>(); const [organizations, setOrganizations] = useState<Organization[]>([])
  const [editing, setEditing] = useState<(AdminUser & { password: string })>()
  // Escape closes this dialog now, so a half-finished account edit needs a
  // question before it is thrown away.
  const userChanged = () => {
    if (!editing) return false
    const original = users?.find(user => user.id === editing.id) ?? reviewers.find(user => user.id === editing.id)
    if (!original) return true
    return editing.password.trim() !== ''
      || editing.displayName !== original.displayName
      || editing.email !== original.email
      || editing.role !== original.role
      || editing.organizationId !== original.organizationId
      || editing.managerId !== original.managerId
      || editing.active !== original.active
  }
  // Every way out of this dialog asks the same question. The × was the one that
  // did not, and it is the one people reach for.
  const confirmDiscardUser = () => !userChanged() || confirm('수정한 내용을 저장하지 않고 닫으시겠습니까?')
  const [form, setForm] = useState({ username: '', displayName: '', email: '', password: '', role: 'USER', organizationId: '' })
  // userPageDefault on the server, sent explicitly so the pager's arithmetic
  // and the page it is describing cannot drift apart.
  const userLimit = 100
  const [total, setTotal] = useState(0)
  const [query, setQuery] = useState('')
  // The list is a page now, so the 검토 책임자 picker cannot be built from it —
  // a reviewer on page three would simply not be offered. Reviewers are asked
  // for by role instead, which is a small set however large the directory.
  const [reviewers, setReviewers] = useState<AdminUser[]>([])
  // Accounts with no organisation are invisible to every team leader: their
  // reports never reach a review queue. Nothing used to say how many there
  // were, and the directory had no way to list them — an administrator would
  // have had to page through everybody looking for a blank column.
  const [unassigned, setUnassigned] = useState(0)
  const [onlyUnassigned, setOnlyUnassigned] = useState(false)
  // The cap was paired with a search, on the reasoning that somebody who needs
  // an account can look it up. That covers finding one account and not the
  // other half of this screen's job — reading down the directory to see who
  // has no reviewer, who is still active, who was given which role. Measured
  // on a 305-account deployment: the card said "사용자 305명 중 100명" and no
  // click reached the other 205, among them 8 team leaders, 3 org managers and
  // an administrator. The server has taken offset all along; the screen never
  // sent one. 감사 로그, in this same file, has had 이전·다음 for as long.
  const [offset, setOffset] = useState(0)
  const load = (search = query, unassignedOnly = onlyUnassigned, from = offset) => Promise.all([
    api<AdminUserPage>(`/api/v1/admin/users?q=${encodeURIComponent(search)}${unassignedOnly ? '&organization=none' : ''}&limit=${userLimit}&offset=${from}`),
    api<Organization[]>('/api/v1/admin/organizations'),
    api<AdminUserPage>('/api/v1/admin/users?role=TEAM_LEADER,ORG_MANAGER,ADMIN&limit=500'),
  ]).then(([page,o,leads]) => { setUsers(page.items); setTotal(page.total); setUnassigned(page.unassigned); setOrganizations(o); setReviewers(leads.items) })
  const goToPage = (next: number) => { setOffset(next); void load(query, onlyUnassigned, next) }
  useEffect(() => { load() }, [])
  useEffect(() => {
    // Typed searches are debounced: one request per keystroke over a directory
    // of thousands is a lot of work to throw away.
    // Back to the first page with the search: a narrowed directory has fewer
    // pages than the one being read, and page three of a two-result search is
    // a blank table under a filled-in search box.
    const timer = setTimeout(() => { setOffset(0); void load(query, onlyUnassigned, 0) }, 300)
    return () => clearTimeout(timer)
  }, [query, onlyUnassigned])
  const create = async () => { try { await post('/api/v1/admin/users', { ...form, organizationId: form.organizationId ? Number(form.organizationId) : null }); setForm({ username: '', displayName: '', email: '', password: '', role: 'USER', organizationId: '' }); await load(); notify('사용자를 등록했습니다.') } catch (error) { notify(errorText(error, '등록할 수 없습니다.'), 'error') } }
  const saveUser = async () => { if (!editing) return; try { await put(`/api/v1/admin/users/${editing.id}`, { displayName: editing.displayName, email: editing.email, password: editing.password, role: editing.role, organizationId: editing.organizationId ?? null, managerId: editing.managerId ?? null, active: editing.active }); setEditing(undefined); await load(); notify('사용자 정보를 저장했습니다.') } catch (error) { notify(errorText(error, '저장할 수 없습니다.'), 'error') } }
  if (!users) return <Spinner/>
  return <><Card title="사용자 등록"><div className="inline-form wrap"><label>아이디<input value={form.username} onChange={e => setForm({...form,username:e.target.value})}/></label><label>표시 이름<input value={form.displayName} onChange={e => setForm({...form,displayName:e.target.value})}/></label><label>이메일<input value={form.email} onChange={e => setForm({...form,email:e.target.value})}/></label><label>초기 비밀번호<input type="password" value={form.password} onChange={e => setForm({...form,password:e.target.value})} placeholder="12자 이상 또는 비움"/></label><label>역할<select value={form.role} onChange={e => setForm({...form,role:e.target.value})}><option value="USER">사용자</option><option value="TEAM_LEADER">팀장</option><option value="ORG_MANAGER">조직장</option><option value="ADMIN">관리자</option></select></label><label>조직<select value={form.organizationId} onChange={e => setForm({...form,organizationId:e.target.value})}><option value="">미지정</option>{organizations.map(o => <option value={o.id} key={o.id}>{o.name}</option>)}</select></label><Button onClick={create}>등록</Button></div></Card><Card title={`사용자 ${total.toLocaleString()}명`} action={<input className="search-inline" value={query} onChange={e => setQuery(e.target.value)} placeholder="아이디·이름·이메일 검색"/>}>
    {unassigned > 0 && <p className="capture-warning">소속 조직이 지정되지 않은 계정이 <strong>{unassigned}명</strong> 있습니다. 이 계정의 주간보고는 어느 팀장에게도 보이지 않아, 제출해도 검토 대기 상태로 남습니다.{' '}
      <button className="text-button" onClick={() => setOnlyUnassigned(!onlyUnassigned)}>{onlyUnassigned ? '전체 보기' : '해당 계정만 보기'}</button></p>}
    <div className="table-wrap"><table><thead><tr><th>사용자</th><th>역할</th><th>조직</th><th>키 버전</th><th>최근 로그인</th><th>상태</th><th/></tr></thead><tbody>{users.map(user => <tr key={user.id}><td><strong>{user.displayName}</strong><small className="cell-sub">{user.username} · {user.email}</small></td><td>{user.role}</td><td>{organizations.find(o => o.id === user.organizationId)?.name ?? '-'}</td><td>{user.keyVersion}</td><td>{formatDate(user.lastLoginAt)}</td><td><span className={`dot ${user.active ? 'on' : ''}`}/>{user.active ? '활성' : '비활성'}</td><td><button className="text-button" onClick={() => setEditing({...user,password:''})}>편집</button></td></tr>)}</tbody></table></div>
    {total > users.length && <div className="audit-pager">
      <span className="muted">전체 {total.toLocaleString()}명 중 {users.length ? offset + 1 : 0}~{Math.min(offset + users.length, total)}</span>
      <Button variant="secondary" disabled={offset === 0} onClick={() => goToPage(Math.max(0, offset - userLimit))}>이전</Button>
      <Button variant="secondary" disabled={offset + users.length >= total} onClick={() => goToPage(offset + userLimit)}>다음</Button>
    </div>}</Card>
    {editing && <Modal onClose={() => setEditing(undefined)} beforeClose={confirmDiscardUser} label={`${editing.username} 사용자 편집`}><header><h2>{editing.username} 사용자 편집</h2><button onClick={() => { if (confirmDiscardUser()) setEditing(undefined) }}>×</button></header><div className="modal-form"><label>표시 이름<input value={editing.displayName} onChange={e => setEditing({...editing,displayName:e.target.value})}/></label><label>이메일<input value={editing.email} onChange={e => setEditing({...editing,email:e.target.value})}/></label><label>역할<select value={editing.role} onChange={e => setEditing({...editing,role:e.target.value as AdminUser['role']})}><option value="USER">사용자</option><option value="TEAM_LEADER">팀장</option><option value="ORG_MANAGER">조직장</option><option value="ADMIN">관리자</option></select></label><label>조직<select value={editing.organizationId ?? ''} onChange={e => setEditing({...editing,organizationId:e.target.value?Number(e.target.value):undefined})}><option value="">미지정</option>{organizations.map(o=><option key={o.id} value={o.id}>{o.name}</option>)}</select></label><label>검토 책임자<select value={editing.managerId ?? ''} onChange={e => setEditing({...editing,managerId:e.target.value?Number(e.target.value):undefined})}><option value="">미지정</option>{reviewers.filter(u=>u.id!==editing.id).map(u=><option key={u.id} value={u.id}>{u.displayName}</option>)}</select></label><label>새 비밀번호<input type="password" value={editing.password} onChange={e => setEditing({...editing,password:e.target.value})} placeholder="변경할 때만 12자 이상 입력"/></label><label className="checkbox-line"><input type="checkbox" checked={editing.active} onChange={e=>setEditing({...editing,active:e.target.checked})}/> 활성 계정</label></div><footer><Button variant="secondary" onClick={()=>setEditing(undefined)}>취소</Button><Button onClick={saveUser}>저장</Button></footer></Modal>}
  </>
}

function OrganizationsTab({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [items, setItems] = useState<Organization[]>(); const [name,setName]=useState('');const [code,setCode]=useState('');const [parent,setParent]=useState('')
  const load=()=>api<Organization[]>('/api/v1/admin/organizations').then(setItems);useEffect(()=>{load()},[])
  const create=async()=>{try{await post('/api/v1/admin/organizations',{name,code,parentId:parent?Number(parent):null});setName('');setCode('');setParent('');await load();notify('조직을 등록했습니다.')}catch(error){notify(error instanceof Error?error.message:'등록할 수 없습니다.','error')}}
  // An organisation could be created and then never corrected — not a typo in
  // its name, not a restructure — so whoever had a chart to fix edited the
  // database, which is where a parent_id loop comes from. The server refuses a
  // move that would make one; nothing refused it in the table.
  const [editing,setEditing]=useState<number|undefined>();const [draft,setDraft]=useState({name:'',code:'',parentId:''})
  const startEdit=(org:Organization)=>{setEditing(org.id);setDraft({name:org.name,code:org.code,parentId:org.parentId?String(org.parentId):''})}
  const saveEdit=async()=>{if(editing===undefined)return;try{await patch(`/api/v1/admin/organizations/${editing}`,{name:draft.name,code:draft.code,parentId:draft.parentId?Number(draft.parentId):null});setEditing(undefined);await load();notify('조직을 수정했습니다.')}catch(error){notify(error instanceof Error?error.message:'수정할 수 없습니다.','error')}}
  if(!items)return <Spinner/>;return <><Card title="조직 등록"><div className="inline-form"><label>조직명<input value={name} onChange={e=>setName(e.target.value)}/></label><label>코드<input value={code} onChange={e=>setCode(e.target.value.toUpperCase())}/></label><label>상위 조직<select value={parent} onChange={e=>setParent(e.target.value)}><option value="">최상위</option>{items.map(o=><option key={o.id} value={o.id}>{o.name}</option>)}</select></label><Button onClick={create}>등록</Button></div></Card><Card title="조직 구조"><div className="org-list">{items.map(org=>editing===org.id
    ? <div key={org.id} className="org-editing"><span className="org-icon">▦</span><div className="inline-form wrap"><label>조직명<input value={draft.name} onChange={e=>setDraft({...draft,name:e.target.value})}/></label><label>코드<input value={draft.code} onChange={e=>setDraft({...draft,code:e.target.value.toUpperCase()})}/></label><label>상위 조직<select value={draft.parentId} onChange={e=>setDraft({...draft,parentId:e.target.value})}><option value="">최상위</option>{items.filter(o=>o.id!==org.id).map(o=><option key={o.id} value={o.id}>{o.name}</option>)}</select></label><Button onClick={saveEdit}>저장</Button><Button variant="ghost" onClick={()=>setEditing(undefined)}>취소</Button></div></div>
    : <div key={org.id}><span className="org-icon">▦</span><div><strong>{org.name}</strong><small>{org.code} · 구성원 {org.userCount}명</small></div><span className="org-parent">{org.parentId?`상위: ${items.find(o=>o.id===org.parentId)?.name??'-'}`:'최상위'}</span><Button variant="ghost" onClick={()=>startEdit(org)}>수정</Button></div>)}</div></Card></>
}

/**
 * The export template, per organisation.
 *
 * One deployment used to mean one deck. That holds until two divisions have
 * their own report format, and then everybody exports somebody else's cover
 * page. The selector picks who a template is for; a team that sets none uses
 * its division's, and a division that sets none uses the house one — the same
 * walk the export itself does, shown here so the answer is readable before
 * anyone exports anything.
 */
function PPTXTab({ notify }: { notify: (message: string, kind?: 'success' | 'error') => void }) {
  const [orgs,setOrgs]=useState<Organization[]>([])
  const [target,setTarget]=useState('')
  const [info,setInfo]=useState<TemplateInfo>();const [file,setFile]=useState<File>();const [busy,setBusy]=useState(false)
  const query=target?`?organizationId=${target}`:''
  const load=()=>api<TemplateInfo>(`/api/v1/admin/pptx-template${query}`).then(setInfo)
  useEffect(()=>{api<Organization[]>('/api/v1/admin/organizations').then(setOrgs)},[])
  useEffect(()=>{setInfo(undefined);setFile(undefined);load()},[target])
  const scopeName=target?(orgs.find(o=>String(o.id)===target)?.name??'선택한 조직'):'전사 기본'
  const upload=async()=>{if(!file)return;setBusy(true);try{const body=new FormData();body.append('file',file);await api(`/api/v1/admin/pptx-template${query}`,{method:'POST',body});await load();setFile(undefined);notify(`${scopeName} PPTX 템플릿을 적용했습니다.`)}catch(error){notify(error instanceof Error?error.message:'업로드할 수 없습니다.','error')}finally{setBusy(false)}}
  const reset=async()=>{if(!confirm(target?`${scopeName} 전용 서식을 삭제하고 상위 조직 서식을 따르게 하시겠습니까?`:'사용자 템플릿을 삭제하고 기본 템플릿으로 되돌리시겠습니까?'))return;await del(`/api/v1/admin/pptx-template${query}`);await load();notify(target?`${scopeName} 전용 서식을 해제했습니다.`:'기본 PPTX 템플릿으로 초기화했습니다.')}
  const ownTemplate=info!==undefined&&(target?info.source==='organization':info.source==='custom')
  const sourceLabel=(value:TemplateInfo)=>value.source==='organization'?`${scopeName} 전용 서식`
    :value.source==='inherited'?`${value.inheritedFrom} 서식을 그대로 사용`
    :value.source==='custom'?(target?'전사 기본 서식을 그대로 사용':'관리자 템플릿')
    :(target?'Weekly 기본 템플릿을 그대로 사용':'Weekly 기본 템플릿')
  return <><Card title="적용 대상" action={<span className="muted-chip">{orgs.length}개 조직</span>}>
      <div className="inline-form"><label>템플릿을 지정할 조직<select value={target} onChange={e=>setTarget(e.target.value)}>
        <option value="">전사 기본 (지정하지 않은 모든 조직)</option>
        {orgs.map(o=><option key={o.id} value={o.id}>{o.name}{o.parentId?` (상위: ${orgs.find(x=>x.id===o.parentId)?.name??'-'})`:''}</option>)}
      </select></label></div>
      <p className="muted">보고서는 <b>작성자가 속한 조직</b>의 서식으로 내보냅니다. 전용 서식이 없으면 상위 조직의 서식을, 그것도 없으면 전사 기본을 씁니다.</p>
    </Card>
    {info===undefined?<Spinner/>:<><Card title={`${scopeName} 내보내기 템플릿`}><div className="template-info"><span className="ppt-icon">P</span><div><strong>{info.originalName}</strong><p>{sourceLabel(info)} · {(info.sizeBytes/1024).toFixed(1)} KB {info.uploadedAt&&`· ${formatDate(info.uploadedAt)}`}</p><code>SHA-256 {info.sha256.slice(0,16)}…</code></div>{ownTemplate&&<Button variant="danger" onClick={reset}>{target?'전용 서식 해제':'기본값 복원'}</Button>}</div><div className="placeholder-list">{info.placeholders.map(token=><code key={token}>{token}</code>)}</div></Card><Card title={target?`${scopeName} 전용 서식 등록`:`${scopeName} 서식 등록`}><p className="muted">기준 PPTX의 디자인은 그대로 유지됩니다. 바뀔 텍스트 상자를 아래 토큰으로 교체한 뒤 업로드하세요. <b>{'{{THIS_WEEK}}'}</b>와 <b>{'{{NEXT_WEEK}}'}</b>는 필수입니다.</p><div className="token-guide"><div><code>{'{{WEEK_SCHEDULE}}'}</code><span>주간 일정</span></div><div><code>{'{{THIS_WEEK}}'}</code><span>이번 주 한 일 목록</span></div><div><code>{'{{NEXT_WEEK}}'}</code><span>다음 주 할 일 목록</span></div><div><code>{'{{ISSUES}}'}</code><span>이슈 목록</span></div><div><code>{'{{AUTHOR}}'} · {'{{TEAM}}'}</code><span>작성자 · 조직</span></div></div><div className="upload-row"><input type="file" accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation" onChange={e=>setFile(e.target.files?.[0])}/><Button onClick={upload} disabled={!file||busy}>{busy?'검증·적용 중…':'템플릿 적용'}</Button></div></Card></>}
  </>
}

type AuditPage = { items: Record<string, unknown>[]; total: number; limit: number; offset: number; retentionDays: number }

/**
 * The audit trail, queried rather than tailed. It used to show the last 500
 * entries with no way to narrow them, which for a few hundred people meant the
 * answer to "who approved this, and when" left the screen within days.
 */
function AuditTab() {
  const [page, setPage] = useState<AuditPage>()
  const [filters, setFilters] = useState({ action: '', actor: '', from: '', to: '' })
  const [applied, setApplied] = useState({ action: '', actor: '', from: '', to: '' })
  const [offset, setOffset] = useState(0)
  const [failure, setFailure] = useState('')
  const limit = 50
  useEffect(() => {
    // Sequenced, because two quick filter submissions can come back out of
    // order and leave the older result on screen under the newer filters.
    let current = true
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    for (const [key, value] of Object.entries(applied)) if (value.trim()) query.set(key, value.trim())
    setFailure('')
    api<AuditPage>(`/api/v1/admin/audit?${query}`)
      .then(value => { if (current) setPage(value) })
      // A failure used to leave the spinner turning forever with nothing said.
      .catch(error => { if (current) setFailure(errorText(error, '감사 로그를 조회할 수 없습니다.')) })
    return () => { current = false }
  }, [applied, offset])
  const apply = (event: React.FormEvent) => { event.preventDefault(); setOffset(0); setApplied(filters) }
  if (failure) return <Card title="감사 로그"><Empty>{failure}</Empty><div className="audit-pager">
    <Button variant="secondary" onClick={() => setApplied({ ...applied })}>다시 시도</Button></div></Card>
  if (!page) return <Spinner/>
  const shown = page.items.length
  const last = Math.min(offset + shown, page.total)
  return <Card title="감사 로그" action={<span className="muted">{page.retentionDays > 0 ? `${page.retentionDays}일 보관` : '무기한 보관'}</span>}>
    <form className="audit-filters" onSubmit={apply}>
      <label>작업<input value={filters.action} onChange={e => setFilters({ ...filters, action: e.target.value })} placeholder="report.approve"/></label>
      <label>행위자<input value={filters.actor} onChange={e => setFilters({ ...filters, actor: e.target.value })} placeholder="이름 또는 아이디"/></label>
      <label>시작일<input type="date" value={filters.from} onChange={e => setFilters({ ...filters, from: e.target.value })}/></label>
      <label>종료일<input type="date" value={filters.to} onChange={e => setFilters({ ...filters, to: e.target.value })}/></label>
      <Button>조회</Button>
    </form>
    {!shown ? <Empty>조건에 맞는 감사 로그가 없습니다.</Empty> : <>
      <div className="table-wrap"><table>
        <thead><tr><th>시간</th><th>행위자</th><th>작업</th><th>대상</th><th>IP</th></tr></thead>
        <tbody>{page.items.map((log, index) => <tr key={index}>
          <td>{formatDate(String(log.createdAt))}</td>
          <td>{String(log.actor)}</td>
          <td><code>{String(log.action)}</code></td>
          <td>{String(log.resourceType)} / {String(log.resourceId)}</td>
          <td>{String(log.ipAddress ?? '-')}</td>
        </tr>)}</tbody>
      </table></div>
      <div className="audit-pager">
        <span className="muted">전체 {page.total.toLocaleString()}건 중 {offset + 1}~{last}</span>
        <Button variant="secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))}>이전</Button>
        <Button variant="secondary" disabled={last >= page.total} onClick={() => setOffset(offset + limit)}>다음</Button>
      </div>
    </>}
  </Card>
}
