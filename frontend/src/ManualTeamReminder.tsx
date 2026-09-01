import { useRef, useState } from 'react'
import { APIError, errorText, post } from './api'
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

export function manualTeamReminderButtonState(relayReady: boolean, busy: boolean) {
  return {
    disabled: !relayReady || busy,
    label: busy ? '대상 확인·큐 추가 중…' : '지금 권고 메일 보내기',
  }
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
  const [failed, setFailed] = useState('')
  const inFlight = useRef(false)

  if (!available) return null
  const send = async () => {
    // State is rendered later. The ref changes synchronously, so two clicks in
    // the same render tick cannot both reach the endpoint.
    if (!claimManualTeamReminderRun(inFlight)) return
    if (!confirmManualTeamReminder(() => window.confirm(MANUAL_TEAM_REMINDER_CONFIRMATION))) {
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
    }
  }

  const button = manualTeamReminderButtonState(relayReady, busy)

  return <section className="manual-reminder" aria-busy={busy} aria-labelledby="manual-team-reminder-heading">
    <header className="manual-reminder-head">
      <h3 id="manual-team-reminder-heading">수동 발송</h3>
      <p>자동 발송 사용 여부·요일과 독립적으로, 누르는 시점의 이번 주 미제출 팀원을 다시 확인해 발송 대기열에 추가합니다. 자동 설정 값은 바뀌지 않으며 별도 저장도 필요하지 않습니다.</p>
    </header>
    {!relayReady && <div className="edit-notice">관리자가 메일 서버를 준비하기 전에는 지금 발송할 수 없습니다.</div>}
    <div className="manual-reminder-actions"><Button variant="secondary" disabled={button.disabled} onClick={() => { void send() }}>
      {button.label}
    </Button></div>
    {failed && <p className="manual-reminder-error" role="alert">{failed}</p>}
    {result && <ManualTeamReminderResult result={result}/>}
  </section>
}
