import { describe, expect, it } from 'vitest'
import { handoffOriginLabel, handoffOutcome, handoffRequest } from './handoff'

describe('handoffRequest', () => {
  it('reads a source and claim out of the route', () => {
    expect(handoffRequest({ source: 'https://ptium.intra', claim: 'abc' })).toEqual({ source: 'https://ptium.intra', claim: 'abc' })
  })
  it('is nothing when either half is missing', () => {
    expect(handoffRequest({ source: 'https://ptium.intra' })).toBeUndefined()
    expect(handoffRequest({ claim: 'abc' })).toBeUndefined()
    expect(handoffRequest(undefined)).toBeUndefined()
    expect(handoffRequest({ source: ' ', claim: 'abc' })).toBeUndefined()
  })
})

describe('handoffOriginLabel', () => {
  it('shows the host, with its port, and never the scheme', () => {
    expect(handoffOriginLabel('https://ptium.intra')).toBe('ptium.intra')
    expect(handoffOriginLabel('http://umm.intra:8080/')).toBe('umm.intra:8080')
  })
  it('falls back to the raw value when it is not an address', () => {
    expect(handoffOriginLabel('not an address')).toBe('not an address')
  })
})

describe('handoffOutcome', () => {
  it('names the origin and the file when it arrived', () => {
    const outcome = handoffOutcome('received', 'https://ptium.intra', '3분기.pptx')
    expect(outcome.state).toBe('received')
    expect(outcome.text).toContain('ptium.intra')
    expect(outcome.text).toContain('3분기.pptx')
  })
  it('repeats the server sentence and says the claim is spent when it failed', () => {
    const outcome = handoffOutcome('failed', 'https://ptium.intra', '표가 만료됐거나 이미 쓰였습니다.')
    expect(outcome.state).toBe('failed')
    expect(outcome.text).toContain('표가 만료됐거나 이미 쓰였습니다.')
    expect(outcome.text).toContain('다시 보내')
  })
})
