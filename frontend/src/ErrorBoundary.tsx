import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'

/**
 * A rendering failure used to blank the entire application: no message, no way
 * back, and nothing on screen to report. This keeps the failure inside the
 * screen that caused it and says what to do next.
 *
 * It deliberately does not try to recover by itself. Re-rendering the same
 * state would fail the same way, so the choice offered is the one that can
 * actually work: go back to a different screen, or reload.
 */
export default class ErrorBoundary extends Component<
  { children: ReactNode; onReset?: () => void },
  { message: string | null }
> {
  state = { message: null as string | null }

  static getDerivedStateFromError(error: unknown) {
    return { message: error instanceof Error ? error.message : String(error) }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The console is the only place a user can copy this from in an offline
    // deployment with no error reporting service.
    console.error('화면을 표시하는 중 오류가 발생했습니다.', error, info.componentStack)
  }

  render() {
    if (this.state.message === null) return this.props.children
    return <div className="screen-error">
      <h2>이 화면을 표시할 수 없습니다</h2>
      <p>다른 화면은 정상일 수 있습니다. 계속 반복되면 아래 내용을 관리자에게 알려 주세요.</p>
      <code>{this.state.message}</code>
      <div className="screen-error-actions">
        <button className="button secondary" onClick={() => { this.setState({ message: null }); this.props.onReset?.() }}>대시보드로</button>
        <button className="button" onClick={() => window.location.reload()}>새로고침</button>
      </div>
    </div>
  }
}
