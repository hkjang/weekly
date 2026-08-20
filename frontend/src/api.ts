import { reportSessionLost } from './session'

interface Envelope<T> { success: boolean; data: T; error?: { code: string; message: string }; traceId: string }

export class APIError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) { super(message); this.status = status; this.code = code }
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
    throw new APIError(response.status, payload?.error?.code ?? 'REQUEST_FAILED', payload?.error?.message ?? '요청을 처리할 수 없습니다.')
  }
  return payload.data
}

export const post = <T>(url: string, data?: unknown) => api<T>(url, { method: 'POST', body: JSON.stringify(data ?? {}) })
export const put = <T>(url: string, data: unknown) => api<T>(url, { method: 'PUT', body: JSON.stringify(data) })
export const patch = <T>(url: string, data: unknown) => api<T>(url, { method: 'PATCH', body: JSON.stringify(data) })
export const del = <T>(url: string) => api<T>(url, { method: 'DELETE' })
