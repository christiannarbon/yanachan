import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
// decideAuthFailure is pure; reauthenticate keeps module state, so the tests
// that use it load a fresh copy with load().
import { decideAuthFailure } from './reauth'

type Storage = { getItem: (k: string) => string | null; setItem: (k: string, v: string) => void }

function memoryStorage(): Storage {
  const m = new Map<string, string>()
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v) }
}

function blockedStorage(): Storage {
  return {
    getItem: () => {
      throw new Error('blocked')
    },
    setItem: () => {
      throw new Error('blocked')
    },
  }
}

let assign: ReturnType<typeof vi.fn>
let listeners: Record<string, Array<() => void>>
let visibility: 'visible' | 'hidden'

function setup(storage: Storage, path = '/board/mine') {
  assign = vi.fn()
  listeners = {}
  visibility = 'visible'
  vi.stubGlobal('sessionStorage', storage)
  vi.stubGlobal('location', { pathname: path })
  vi.stubGlobal('window', { location: { assign } })
  vi.stubGlobal('document', {
    get visibilityState() {
      return visibility
    },
    addEventListener: (type: string, fn: () => void) => (listeners[type] ??= []).push(fn),
    removeEventListener: (type: string, fn: () => void) =>
      (listeners[type] = (listeners[type] ?? []).filter((f) => f !== fn)),
  })
}

async function load() {
  vi.resetModules()
  return import('./reauth')
}

beforeEach(() => vi.useFakeTimers({ now: new Date('2026-11-09T09:00:00Z') }))
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('returnPath', () => {
  it.each([
    ['/board/team-x', '/board/team-x'],
    ['/', '/'],
    ['//evil.example', '/'],
    ['/api/auth/login', '/'],
    ['/api', '/'],
    ['/apiary', '/apiary'],
    ['/a?b', '/'],
    ['/a/../b', '/'],
  ])('%s → %s', async (input, want) => {
    setup(memoryStorage())
    const { returnPath } = await load()
    expect(returnPath(input)).toBe(want)
  })
})

describe('reauthenticate', () => {
  it('goes now when the tab is visible, with the current path', async () => {
    setup(memoryStorage(), '/board/team-x')
    const { reauthenticate } = await load()
    expect(reauthenticate('/api/auth/login')).toBe(true)
    expect(assign).toHaveBeenCalledWith('/api/auth/login?return=%2Fboard%2Fteam-x')
  })

  it('refuses a second trip within a minute, and allows one after', async () => {
    const storage = memoryStorage()
    setup(storage)
    let { reauthenticate } = await load()
    expect(reauthenticate('/api/auth/login')).toBe(true)

    // The next page load, back from GitHub.
    ;({ reauthenticate } = await load())
    vi.advanceTimersByTime(30_000)
    expect(reauthenticate('/api/auth/login')).toBe(false)
    vi.advanceTimersByTime(31_000)
    expect(reauthenticate('/api/auth/login')).toBe(true)
    expect(assign).toHaveBeenCalledTimes(2)
  })

  it('waits for a hidden tab to become visible, and goes once', async () => {
    setup(memoryStorage())
    const { reauthenticate } = await load()
    visibility = 'hidden'
    expect(reauthenticate('/api/auth/login')).toBe(true)
    expect(reauthenticate('/api/auth/login')).toBe(true)
    expect(assign).not.toHaveBeenCalled()
    visibility = 'visible'
    for (const fn of [...(listeners.visibilitychange ?? [])]) fn()
    expect(assign).toHaveBeenCalledTimes(1)
  })

  it('never sends the browser back into the API', async () => {
    setup(memoryStorage(), '/api/auth/login')
    const { reauthenticate } = await load()
    reauthenticate('/api/auth/login')
    expect(assign).toHaveBeenCalledWith('/api/auth/login?return=%2F')
  })
})

describe('decideAuthFailure', () => {
  const state = { pending: false, loginUrl: '/api/auth/login', authError: '' }
  const yes = () => true
  const no = () => false

  it('leaves anything but a 401 to the caller', () => {
    expect(decideAuthFailure({ status: 502 }, state, yes)).toBe('other')
    expect(decideAuthFailure(null, state, yes)).toBe('other')
  })

  it('does nothing while a renewal is under way', () => {
    expect(decideAuthFailure({ status: 401, reauth: true }, { ...state, pending: true }, yes)).toBe('pending')
  })

  it('goes back to the gate when the session is gone', () => {
    expect(decideAuthFailure({ status: 401 }, state, yes)).toBe('gate')
    expect(decideAuthFailure({ status: 401, reauth: true }, { ...state, loginUrl: undefined }, yes)).toBe('gate')
  })

  it('renews when asked to and the guard allows it', () => {
    expect(decideAuthFailure({ status: 401, reauth: true }, state, yes)).toBe('renew')
  })

  it('offers a way out when the guard refuses, instead of a dead end', () => {
    expect(decideAuthFailure({ status: 401, reauth: true }, state, no)).toBe('stuck')
  })

  it('never goes straight back after a failed renewal, even with storage blocked', () => {
    const renew = vi.fn(() => true)
    expect(decideAuthFailure({ status: 401, reauth: true }, { ...state, authError: 'denied' }, renew)).toBe('stuck')
    expect(renew).not.toHaveBeenCalled()
  })
})

describe('blocked storage', () => {
  it('still renews once per page load', async () => {
    setup(blockedStorage())
    const { reauthenticate } = await load()
    expect(reauthenticate('/api/auth/login')).toBe(true)
    expect(assign).toHaveBeenCalledTimes(1)
  })
})
