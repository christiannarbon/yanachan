import type {
  AuthStatus,
  Board,
  DevicePoll,
  DeviceStart,
  SessionView,
  Settings,
  Stats,
  Suggestions,
} from './types'

export class ApiError extends Error {
  status: number
  /** The server wants the browser to pass through GitHub to renew its token. */
  reauth: boolean
  constructor(message: string, status: number, reauth = false) {
    super(message)
    this.status = status
    this.reauth = reauth
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  const text = await res.text()
  let body: unknown = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      body = { error: text }
    }
  }
  if (!res.ok) {
    const message =
      body && typeof body === 'object' && 'error' in body
        ? String((body as { error: unknown }).error)
        : `request failed with ${res.status}`
    const reauth = !!body && typeof body === 'object' && (body as { reauth?: unknown }).reauth === true
    throw new ApiError(message, res.status, reauth)
  }
  return body as T
}

export const api = {
  authStatus: () => request<AuthStatus>('/api/auth/status'),
  approveGhCli: () => request<SessionView>('/api/auth/gh-cli/approve', { method: 'POST' }),
  approveEnvToken: () => request<SessionView>('/api/auth/env-token/approve', { method: 'POST' }),
  deviceStart: () => request<DeviceStart>('/api/auth/device/start', { method: 'POST' }),
  devicePoll: () => request<DevicePoll>('/api/auth/device/poll', { method: 'POST' }),
  logout: () => request<{ status: string }>('/api/auth/logout', { method: 'POST' }),

  settings: () => request<Settings>('/api/settings'),
  saveSettings: (s: Settings) => request<Settings>('/api/settings', { method: 'PUT', body: JSON.stringify(s) }),
  suggestions: () => request<Suggestions>('/api/suggestions'),

  stats: () => request<Stats>('/api/stats'),

  board: (opts: { onlyActive?: boolean } = {}) => {
    const params = new URLSearchParams()
    if (opts.onlyActive !== undefined) params.set('onlyActive', String(opts.onlyActive))
    const qs = params.toString()
    return request<Board>(`/api/board${qs ? `?${qs}` : ''}`)
  },
}
