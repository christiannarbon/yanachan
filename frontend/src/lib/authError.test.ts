import { afterEach, describe, expect, it, vi } from 'vitest'
import { authErrorKey, takeAuthError } from './authError'

function stubURL(search: string, path = '/board/mine', hash = '') {
  const replaceState = vi.fn()
  vi.stubGlobal('location', { search, pathname: path, hash })
  vi.stubGlobal('history', { replaceState })
  return replaceState
}

afterEach(() => vi.unstubAllGlobals())

describe('takeAuthError', () => {
  it('returns nothing and leaves the URL alone without the parameter', () => {
    const replaceState = stubURL('?x=1')
    expect(takeAuthError()).toBe('')
    expect(replaceState).not.toHaveBeenCalled()
  })

  it('reads a known code and strips only that parameter', () => {
    const replaceState = stubURL('?authError=not_allowed&x=1', '/board/mine', '#top')
    expect(takeAuthError()).toBe('not_allowed')
    expect(replaceState).toHaveBeenCalledWith(null, '', '/board/mine?x=1#top')
  })

  it('maps an unknown code to server, so nothing from the URL is shown raw', () => {
    stubURL('?authError=<script>')
    expect(takeAuthError()).toBe('server')
  })
})

describe('authErrorKey', () => {
  it.each([
    ['denied', 'errDenied'],
    ['state', 'errState'],
    ['exchange', 'errExchange'],
    ['not_allowed', 'errNotAllowed'],
    ['server', 'errServer'],
    ['', ''],
  ] as const)('%s → %s', (code, key) => {
    expect(authErrorKey(code)).toBe(key)
  })
})
