import { useEffect, useRef, useState } from 'react'
import { APIError, api, errorText, post } from './api'
import { Button } from './components'
import type { TeamReminderQueueResult } from './types'

export const MANUAL_TEAM_REMINDER_CONFIRMATION =
  '자동 발송 설정과 관계없이 이번 주 미제출 팀원을 다시 확인해 권고 메일을 발송 대기열에 추가하시겠습니까?\n이미 발송했거나 대기 중인 사람에게는 중복으로 추가하지 않습니다.'

export function confirmManualTeamReminder(confirmSend: () => boolean): boolean {
  return confirmSend()
}

export function claimManualTeamReminderRun(guard: { current: boolean }): boolean {
  if (guard.current) return false
  guard.current = true
  return true
}

export function queueManualTeamReminder(): Promise<TeamReminderQueueResult> {
  return post<TeamReminderQueueResult>('/api/v1/me/team-reminders')
}

/** 지금 누르면 무엇이 큐에 들어가는지. 서버가 같은 트랜잭션을 돌리고 되돌린 결과입니다. */
export function previewManualTeamReminder(): Promise<TeamReminderQueueResult> {
  return api<TeamReminderQueueResult>('/api/v1/me/team-reminders/preview')
}

export function manualTeamReminderButtonState(relayReady: boolean, busy: boolean, preview?: TeamReminderQueueResult) {
  // A preview that says nobody would receive it is a button that would do
  // nothing; it says so instead of letting the click find out.
  const nobody = !!preview && preview.queued === 0
  return {
    disabled: !relayReady || busy || nobody,
    label: busy ? '대상 확인·큐 추가 중…' : nobody ? '지금 보낼 대상이 없습니다' : '지금 권고 메일 보내기',
  }
}

/** 확인 창 문구. 미리보기가 있으면 몇 명에게 가는지부터 말합니다. */
export function manualTeamReminderConfirmation(preview?: TeamReminderQueueResult): string {
  if (!preview) return MANUAL_TEAM_REMINDER_CONFIRMATION
  return `이번 주 미제출 팀원 중 ${preview.queued}명에게 작성 권고 메일을 보냅니다.\n` + MANUAL_TEAM_REMINDER_CONFIRMATION
}

/** 미리보기 한 줄: 누가 왜 빠지는지까지. 이름은 싣지 않습니다 — 누가 수신을 껐는지는 그 사람의 일입니다. */
export function manualTeamReminderPreviewSummary(preview: TeamReminderQueueResult): string {
  const parts = [`이번 주 미제출 ${preview.eligible}명`]
  if (preview.skippedNotOptedIn > 0) parts.push(`수신 안 함 ${preview.skippedNotOptedIn}명`)
  if (preview.skippedExcluded > 0) parts.push(`발송 제외 조직 ${preview.skippedExcluded}명`)
  if (preview.alreadyQueued > 0) parts.push(`이미 발송·대기 ${preview.alreadyQueued}명`)
  if (preview.skippedNoAddress > 0) parts.push(`주소 없음 ${preview.skippedNoAddress}명`)
  return parts.join(' · ')
}

export function ManualTeamReminderPreview({ preview }: { preview: TeamReminderQueueResult }) {
  return <div className="manual-reminder-preview" aria-live="polite">
    <strong>지금 보내면 {preview.queued}명에게 갑니다.</strong>
    <small>{manualTeamReminderPreviewSummary(preview)}</small>
    {preview.queued === 0 && preview.skippedNotOptedIn > 0 &&
      <small>권고 메일은 팀원이 개인 설정에서 <b>작성 권고 메일 받기</b>를 켠 경우에만 갑니다.</small>}
  </div>
}

export function manualTeamReminderErrorMessage(error: unknown): string {
  if (error instanceof APIError && error.code === 'MAIL_RELAY_NOT_READY') {
    return '관리자가 메일 서버를 준비하지 않아 지금 권고 메일을 보낼 수 없습니다.'
  }
  if (error instanceof APIError && error.code === 'REMINDER_ROLE_REQUIRED') {
    return '팀원에게 권고 메일을 보내려면 팀장 이상의 권한이 필요합니다. 권한이 변경됐다면 화면을 새로고침해 주세요.'
  }
  return errorText(error, '권고 메일을 발송 대기열에 추가할 수 없습니다.')
}

export function manualTeamReminderResultSummary(result: TeamReminderQueueResult): string {
  const messages = result.queued > 0
    ? [`${result.queued}명의 권고 메일을 발송 대기열에 추가했습니다.`]
    : result.eligible === 0
      ? ['현재 권고할 미제출 팀원이 없습니다.']
      : ['새로 발송 대기열에 추가된 메일이 없습니다.']
  if (result.alreadyQueued > 0) messages.push(`${result.alreadyQueued}명은 이번 주에 이미 발송했거나 대기 중이라 중복에서 제외했습니다.`)
  if (result.skippedNoAddress > 0) messages.push(`${result.skippedNoAddress}명은 유효한 메일 주소가 없어 건너뛰었습니다.`)
  if (result.skippedNotOptedIn > 0) messages.push(`${result.skippedNotOptedIn}명은 개인 설정에서 권고 메일 받기를 켜지 않아 보내지 않았습니다.`)
  if (result.skippedExcluded > 0) messages.push(`${result.skippedExcluded}명은 메일 발송 제외 조직 소속이라 보내지 않았습니다.`)
  return messages.join(' ')
}

export function ManualTeamReminderResult({ result }: { result: TeamReminderQueueResult }) {
  return <div className="manual-reminder-result">
    <strong>{result.weekStart} 주차 처리 결과</strong>
    <dl>
      <div><dt>권고 대상</dt><dd>{result.eligible}명</dd></div>
      <div><dt>새로 발송 대기</dt><dd>{result.queued}명</dd></div>
      <div><dt>중복 제외</dt><dd>{result.alreadyQueued}명<small>이미 발송·대기</small></dd></div>
      <div className={result.skippedNoAddress > 0 ? 'warning' : ''}><dt>주소 없음</dt><dd>{result.skippedNoAddress}명</dd></div>
      <div><dt>수신 안 함</dt><dd>{result.skippedNotOptedIn}명<small>본인이 받기를 켜지 않음</small></dd></div>
      <div><dt>발송 제외 조직</dt><dd>{result.skippedExcluded}명</dd></div>
    </dl>
    <p>{manualTeamReminderResultSummary(result)}</p>
  </div>
}

export default function ManualTeamReminder({ available, relayReady, notify }: {
  available: boolean
  relayReady: boolean
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<TeamReminderQueueResult>()
  const [preview, setPreview] = useState<TeamReminderQueueResult>()
  const [failed, setFailed] = useState('')
  const inFlight = useRef(false)
  // A preview that cannot be read leaves the button as it always was: the
  // server still decides, and the result card still tells.
  const loadPreview = () => previewManualTeamReminder().then(setPreview).catch(() => setPreview(undefined))
  useEffect(() => { if (available) void loadPreview() }, [available])

  if (!available) return null
  const send = async () => {
    // State is rendered later. The ref changes synchronously, so two clicks in
    // the same render tick cannot both reach the endpoint.
    if (!claimManualTeamReminderRun(inFlight)) return
    if (!confirmManualTeamReminder(() => window.confirm(manualTeamReminderConfirmation(preview)))) {
      inFlight.current = false
      return
    }
    setBusy(true)
    setFailed('')
    setResult(undefined)
    try {
      const queued = await queueManualTeamReminder()
      setResult(queued)
      // Every count is the result of a successfully accepted request. Address
      // omissions remain visible in the persistent warning tile below instead
      // of turning an otherwise successful queue operation into an error toast.
      notify(manualTeamReminderResultSummary(queued), 'success')
    } catch (error) {
      const message = manualTeamReminderErrorMessage(error)
      setFailed(message)
      notify(message, 'error')
    } finally {
      inFlight.current = false
      setBusy(false)
      void loadPreview()
    }
  }

  const button = manualTeamReminderButtonState(relayReady, busy, preview)

  return <section className="manual-reminder" aria-busy={busy} aria-labelledby="manual-team-reminder-heading">
    <header className="manual-reminder-head">
      <h3 id="manual-team-reminder-heading">수동 발송</h3>
      <p>자동 발송 사용 여부·요일과 독립적으로, 누르는 시점의 이번 주 미제출 팀원을 다시 확인해 발송 대기열에 추가합니다. 자동 설정 값은 바뀌지 않으며 별도 저장도 필요하지 않습니다.</p>
      <p>메일은 개인 설정에서 <strong>작성 권고 메일 받기</strong>를 켠 팀원에게만 가고, 본문에 보낸 사람(권고자) 이름은 들어가지 않습니다.</p>
    </header>
    {!relayReady && <div className="edit-notice">관리자가 메일 서버를 준비하기 전에는 지금 발송할 수 없습니다.</div>}
    {preview && <ManualTeamReminderPreview preview={preview}/>}
    <div className="manual-reminder-actions"><Button variant="secondary" disabled={button.disabled} onClick={() => { void send() }}>
      {button.label}
    </Button></div>
    {failed && <p className="manual-reminder-error" role="alert">{failed}</p>}
    {result && <ManualTeamReminderResult result={result}/>}
  </section>
}
