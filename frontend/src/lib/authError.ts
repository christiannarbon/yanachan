export type AuthErrorCode = 'denied' | 'state' | 'exchange' | 'not_allowed' | 'server'

const CODES: AuthErrorCode[] = ['denied', 'state', 'exchange', 'not_allowed', 'server']

/** Takes ?authError=… off the address bar and returns it, normalised to a
 *  known code. It runs before routing rewrites the URL, which would drop it. */
export function takeAuthError(): AuthErrorCode | '' {
  const params = new URLSearchParams(location.search)
  const raw = params.get('authError')
  if (raw === null) return ''
  params.delete('authError')
  const qs = params.toString()
  history.replaceState(null, '', location.pathname + (qs ? `?${qs}` : '') + location.hash)
  return (CODES as string[]).includes(raw) ? (raw as AuthErrorCode) : 'server'
}
