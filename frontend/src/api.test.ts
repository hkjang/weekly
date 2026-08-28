import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError, NETWORK_UNREACHABLE, REQUEST_TIMEOUT_MS, SERVER_SILENT, UPLOAD_TIMEOUT_MS, api, errorText } from './api'
import { onSessionLost } from './session'

// Every screen goes through this layer, so what it decides is decided once for
// all of them: whether a failure is announced, what a reader is told, and which
// requests are allowed to describe their own body.

type Reply = { status?: number; body?: unknown; raw?: string }

function reply({ status = 200, body, raw }: Reply) {
  const text = raw ?? JSON.stringify(body)
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => {
      if (raw !== undefined) throw new SyntaxError('not json')
      return JSON.parse(text)
    },
  }
}

function stubFetch(...replies: Reply[]) {
  const calls: { url: string; init: RequestInit }[] = []
  let index = 0
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    return reply(replies[Math.min(index++, replies.length - 1)])
  })
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
  onSessionLost(null)
})

describe('요청을 보낼 때', () => {
  it('JSON 본문에는 Content-Type 을 붙인다', async () => {
    const calls = stubFetch({ body: { success: true, data: 1 } })
    await api('/x', { method: 'POST', body: JSON.stringify({ a: 1 }) })
    expect(new Headers(calls[0].init.headers).get('Content-Type')).toBe('application/json')
  })

  it('FormData 에는 Content-Type 을 붙이지 않는다', async () => {
    // The browser has to write this header itself: multipart needs a boundary,
    // and a hand-written 'application/json' would make every attachment and
    // every PPTX upload fail with a message about the wrong thing.
    const calls = stubFetch({ body: { success: true, data: 1 } })
    await api('/x', { method: 'POST', body: new FormData() })
    expect(new Headers(calls[0].init.headers).has('Content-Type')).toBe(false)
  })

  it('부르는 쪽이 정한 Content-Type 을 덮어쓰지 않는다', async () => {
    const calls = stubFetch({ body: { success: true, data: 1 } })
    await api('/x', { method: 'POST', body: 'raw', headers: { 'Content-Type': 'text/plain' } })
    expect(new Headers(calls[0].init.headers).get('Content-Type')).toBe('text/plain')
  })

  it('세션 쿠키를 함께 보낸다', async () => {
    const calls = stubFetch({ body: { success: true, data: 1 } })
    await api('/x')
    expect(calls[0].init.credentials).toBe('same-origin')
  })
})

describe('응답을 읽을 때', () => {
  it('204 는 본문 없이 성공이다', async () => {
    stubFetch({ status: 204 })
    await expect(api('/x')).resolves.toBeUndefined()
  })

  it('봉투가 success:false 면 200 이어도 실패다', async () => {
    // The status line is not the contract; the envelope is. A 200 carrying a
    // refusal used to reach the screen as data.
    stubFetch({ status: 200, body: { success: false, error: { code: 'NOPE', message: '안 됩니다.' } } })
    await expect(api('/x')).rejects.toThrow('안 됩니다.')
  })

  it('실패에는 상태·코드·추적 ID 가 실린다', async () => {
    stubFetch({ status: 500, body: { success: false, error: { code: 'BOOM', message: '터졌습니다.' }, traceId: 'abc123' } })
    await api('/x').then(
      () => expect.unreachable('실패해야 합니다'),
      (error: unknown) => {
        expect(error).toBeInstanceOf(APIError)
        const failure = error as APIError
        expect([failure.status, failure.code, failure.traceId]).toEqual([500, 'BOOM', 'abc123'])
      })
  })

  it('JSON 이 아닌 오류 응답도 던진다', async () => {
    stubFetch({ status: 502, raw: '<html>gateway</html>' })
    await expect(api('/x')).rejects.toBeInstanceOf(APIError)
  })

  it('401 이면 던지기 전에 세션이 끊겼음을 알린다', async () => {
    // Announced rather than only thrown: a screen that forgets a .catch is
    // exactly the one that would otherwise sit on a spinner for ever.
    const heard: string[] = []
    onSessionLost(() => heard.push('lost'))
    stubFetch({ status: 401, body: { success: false, error: { code: 'UNAUTHORIZED', message: '로그인이 필요합니다.' } } })
    await expect(api('/x')).rejects.toThrow()
    expect(heard).toEqual(['lost'])
  })

  it('401 이 아니면 세션이 끊겼다고 하지 않는다', async () => {
    const heard: string[] = []
    onSessionLost(() => heard.push('lost'))
    stubFetch({ status: 403, body: { success: false, error: { code: 'FORBIDDEN', message: '권한이 없습니다.' } } })
    await expect(api('/x')).rejects.toThrow()
    expect(heard).toEqual([])
  })
})

describe('사람에게 보여 줄 문장', () => {
  it('서버만 설명할 수 있는 실패에는 추적 ID 를 붙인다', () => {
    const failure = new APIError(500, 'BOOM', '저장할 수 없습니다.', 'trace-9')
    expect(errorText(failure, '기본')).toBe('저장할 수 없습니다. (추적 ID trace-9)')
  })

  it('사용자가 고칠 수 있는 실패에는 붙이지 않는다', () => {
    // A wrong password does not need a trace identifier, and the person reading
    // it is not the person who would look one up.
    const failure = new APIError(400, 'INVALID', '비밀번호가 올바르지 않습니다.', 'trace-9')
    expect(errorText(failure, '기본')).toBe('비밀번호가 올바르지 않습니다.')
  })

  it('추적 ID 가 없는 서버 오류는 문장만 보여 준다', () => {
    const failure = new APIError(500, 'BOOM', '저장할 수 없습니다.')
    expect(errorText(failure, '기본')).toBe('저장할 수 없습니다.')
  })

  it('API 오류가 아니면 그 오류의 문장을 쓴다', () => {
    expect(errorText(new Error('네트워크가 끊겼습니다.'), '기본')).toBe('네트워크가 끊겼습니다.')
  })

  it('문장이 없으면 준비된 문장을 쓴다', () => {
    expect(errorText('그냥 문자열', '보고서를 저장할 수 없습니다.')).toBe('보고서를 저장할 수 없습니다.')
  })
})

describe('연결이 끊겼을 때', () => {
  it('브라우저의 영어 대신 무슨 일인지 한국어로 말한다', () => {
    // fetch 가 거절하면 TypeError 가 옵니다. 크롬은 "Failed to fetch",
    // 파이어폭스는 "NetworkError...", 사파리는 "Load failed" 입니다.
    for (const message of ['Failed to fetch', 'NetworkError when attempting to fetch resource.', 'Load failed']) {
      expect(errorText(new TypeError(message), '저장할 수 없습니다.')).toBe(NETWORK_UNREACHABLE)
    }
  })

  it('그 문장은 화면의 내용이 남아 있다고 알려 준다', () => {
    expect(NETWORK_UNREACHABLE).toContain('그대로')
    expect(NETWORK_UNREACHABLE).toContain('다시 시도')
  })

  it('제품 밖에서 온 영어 문장은 호출자의 한국어로 바꾼다', () => {
    expect(errorText(new Error('Unexpected token < in JSON'), '보고서를 저장할 수 없습니다.'))
      .toBe('보고서를 저장할 수 없습니다.')
  })
})

describe('서버가 답하지 않을 때', () => {
  it('기다림을 멈추고 무슨 일인지 말한다', () => {
    // AbortSignal.timeout 은 이름이 TimeoutError 인 DOMException 을 던집니다.
    const timedOut = new DOMException('signal timed out', 'TimeoutError')
    expect(errorText(timedOut, '저장할 수 없습니다.')).toBe(SERVER_SILENT)
  })

  it('그 문장은 내용이 남아 있다고, 그리고 다시 해 보라고 알려 준다', () => {
    expect(SERVER_SILENT).toContain('그대로')
    expect(SERVER_SILENT).toContain('다시 시도')
  })

  it('사용자가 취소한 것은 서버 침묵과 구분한다', () => {
    // AbortError 는 화면이 스스로 그만둔 경우입니다. 실패로 알릴 일이 아닙니다.
    const cancelled = new DOMException('aborted', 'AbortError')
    expect(errorText(cancelled, '보고서를 저장할 수 없습니다.')).toBe('보고서를 저장할 수 없습니다.')
  })

  it('기다리는 한계는 잰 최악의 경우보다 넉넉하다', () => {
    // 300명 배포에서 가장 느린 정당한 읽기가 0.4초였습니다.
    expect(REQUEST_TIMEOUT_MS).toBeGreaterThanOrEqual(10_000)
    expect(UPLOAD_TIMEOUT_MS).toBeGreaterThan(REQUEST_TIMEOUT_MS)
  })
})
