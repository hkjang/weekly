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

/**
 * How long a request may take before the screen stops waiting for it.
 *
 * There was no limit. A connection that is accepted and then never answered —
 * a stalled proxy, the ordinary failure on a company network — left the editor
 * on "저장 중…" with the button disabled. Measured: ninety seconds and still
 * counting, no message, no way back except reloading the page, which throws
 * away the draft the save was trying to keep.
 *
 * Thirty seconds against a measured worst case of 0.4s on a 300 person
 * deployment: the slowest read, the year-scope rollup, and the PPTX export all
 * land under half a second. This is not a performance budget; it is the point
 * at which waiting is no longer the honest thing to show somebody.
 *
 * Retrying a save that the server did receive is safe here — the report carries
 * a version, so a second attempt lands on VERSION_CONFLICT rather than
 * overwriting silently.
 */
export const REQUEST_TIMEOUT_MS = 30_000

/**
 * Uploads take as long as the file and the link between them. A 25MB PPTX over
 * a slow office line is not a stalled proxy, so it gets its own bound.
 */
export const UPLOAD_TIMEOUT_MS = 300_000

export async function api<T>(url: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (init?.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const signal = init?.signal
    ?? AbortSignal.timeout(init?.body instanceof FormData ? UPLOAD_TIMEOUT_MS : REQUEST_TIMEOUT_MS)
  const response = await fetch(url, { ...init, headers, signal, credentials: 'same-origin' })
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
  if (error instanceof APIError) {
    if (error.status >= 500 && error.traceId) return `${error.message} (추적 ID ${error.traceId})`
    return error.message
  }
  // fetch itself refused: the request never reached the server, so there is no
  // envelope and no trace id — the browser hands back its own English string.
  // Measured on a deployment by cutting the connection mid-save: the toast read
  // "Failed to fetch" and nothing else. On a company network that is the most
  // likely failure anybody here will ever see, and it was the one sentence in
  // the product not written for the person reading it.
  //
  // What the screen does is already right — the editor keeps what is on it and
  // saving again after the link returns works — so the sentence says that.
  // AbortSignal.timeout fires a DOMException named TimeoutError. The request
  // may well have arrived — the screen simply stopped waiting for an answer.
  if (error instanceof DOMException && error.name === 'TimeoutError') return SERVER_SILENT
  if (error instanceof TypeError) return NETWORK_UNREACHABLE
  // Any other message from outside the product is the platform's, in the
  // platform's language. The caller's Korean fallback is closer to true.
  if (error instanceof Error && /[가-힣]/.test(error.message)) return error.message
  return fallback
}

export const SERVER_SILENT =
  '서버가 시간 안에 답하지 않았습니다. 화면에 있는 내용은 그대로 있으니 다시 시도해 보시고, 계속 같으면 관리자에게 알려 주세요.'

export const NETWORK_UNREACHABLE =
  '서버에 연결하지 못했습니다. 화면에 있는 내용은 그대로 있으니, 연결이 돌아오면 다시 시도하세요.'

export const post = <T>(url: string, data?: unknown) => api<T>(url, { method: 'POST', body: JSON.stringify(data ?? {}) })
export const put = <T>(url: string, data: unknown) => api<T>(url, { method: 'PUT', body: JSON.stringify(data) })
export const patch = <T>(url: string, data: unknown) => api<T>(url, { method: 'PATCH', body: JSON.stringify(data) })
export const del = <T>(url: string) => api<T>(url, { method: 'DELETE' })

/**
 * Fetch a file the way every other request is fetched, then hand it to the
 * browser to save.
 *
 * Exports were plain <a href> links straight at the API. That works while
 * everything is well, and fails badly when it is not: the browser navigates
 * away from the application and renders whatever came back. An expired session
 * — a tab left open over lunch — showed the user
 * `{"success":false,...,"UNAUTHORIZED"...}` as a page, with the app gone from
 * the tab and no way back but the Back button. An empty period rollup did the
 * same with EMPTY_ROLLUP.
 *
 * Going through fetch keeps the app on screen, puts the failure in the same
 * toast as every other failure, and lets the 401 path announce a lost session
 * exactly as it does everywhere else.
 */
export async function download(url: string, fallbackName: string): Promise<void> {
  const response = await fetch(url, { credentials: 'same-origin' })
  if (!response.ok) {
    let payload: Envelope<unknown> | undefined
    try { payload = await response.json() as Envelope<unknown> } catch { /* not JSON */ }
    if (response.status === 401) reportSessionLost()
    throw new APIError(response.status, payload?.error?.code ?? 'DOWNLOAD_FAILED',
      payload?.error?.message ?? '파일을 내려받을 수 없습니다.', payload?.traceId)
  }
  const blob = await response.blob()
  const objectURL = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = objectURL
  link.download = filenameFrom(response.headers.get('Content-Disposition')) || fallbackName
  document.body.appendChild(link)
  link.click()
  link.remove()
  // Released on the next tick: revoking synchronously can cancel the save in
  // browsers that have not finished reading the blob yet.
  setTimeout(() => URL.revokeObjectURL(objectURL), 10_000)
}

/**
 * The server names the file, including the Korean characters in it, so the
 * name it chose is preferred over anything the caller guesses. RFC 5987's
 * filename* comes first because that is the encoded one.
 */
function filenameFrom(disposition: string | null): string {
  if (!disposition) return ''
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
  if (encoded) { try { return decodeURIComponent(encoded[1]) } catch { /* fall through */ } }
  const plain = /filename="?([^";]+)"?/i.exec(disposition)
  return plain ? plain[1] : ''
}
