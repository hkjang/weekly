import { reportSessionLost } from './session'

interface Envelope<T> { success: boolean; data: T; error?: { code: string; message: string }; traceId: string }

export class APIError extends Error {
  status: number
  code: string
  /**
   * The server's identifier for this exact request. It is written to the log
   * with every error, and was being thrown away here — so a user reporting
   * "저장이 안 돼요" left an operator with nothing to search for.
   */
  traceId?: string
  constructor(status: number, code: string, message: string, traceId?: string) {
    super(message); this.status = status; this.code = code; this.traceId = traceId
  }
}

export async function api<T>(url: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (init?.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const response = await fetch(url, { ...init, headers, credentials: 'same-origin' })
  if (response.ok && response.status === 204) return undefined as T
  let payload: Envelope<T> | undefined
  try { payload = await response.json() as Envelope<T> } catch { /* handled below */ }
  if (!response.ok || !payload?.success) {
    // Announced before throwing, so the shell reacts even when the caller does
    // not catch. A screen that forgets a .catch is exactly the case that used to
    // leave a spinner running forever.
    if (response.status === 401) reportSessionLost()
    throw new APIError(response.status, payload?.error?.code ?? 'REQUEST_FAILED',
      payload?.error?.message ?? '요청을 처리할 수 없습니다.', payload?.traceId)
  }
  return payload.data
}

/**
 * The message to put in front of a user, with the trace identifier appended for
 * failures only the server can explain. A wrong password does not need one; a
 * 500 does, and the person who has to look it up is not the one reading it.
 */
export function errorText(error: unknown, fallback: string): string {
  if (!(error instanceof APIError)) return error instanceof Error ? error.message : fallback
  if (error.status >= 500 && error.traceId) return `${error.message} (추적 ID ${error.traceId})`
  return error.message
}

export const post = <T>(url: string, data?: unknown) => api<T>(url, { method: 'POST', body: JSON.stringify(data ?? {}) })
export const put = <T>(url: string, data: unknown) => api<T>(url, { method: 'PUT', body: JSON.stringify(data) })
export const patch = <T>(url: string, data: unknown) => api<T>(url, { method: 'PATCH', body: JSON.stringify(data) })
export const del = <T>(url: string) => api<T>(url, { method: 'DELETE' })
