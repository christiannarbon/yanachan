const KEY = 'yana.reauthAt'
const MIN_GAP_MS = 60_000
/** Mirrors the backend's rule for `return`; the backend still checks it. */
const RETURN_PATH = /^\/[A-Za-z0-9/_-]*$/

let waiting = false

/** The page path to come back to after renewing, or '/' when the path is not
 *  one the backend would accept: not a page path, `//…`, or the API itself. */
export function returnPath(path: string): string {
  if (!RETURN_PATH.test(path) || path.startsWith('//') || path === '/api' || path.startsWith('/api/')) {
    return '/'
  }
  return path
}

/** Sends the browser through GitHub to renew its token and back to the current
 *  path. Waits for the tab to be visible, and refuses to go twice within a
 *  minute so a failing round-trip cannot loop. Returns false when it refused,
 *  so the caller shows a way out instead. */
export function reauthenticate(loginUrl: string): boolean {
  if (waiting) return true
  let last = 0
  try {
    last = Number(sessionStorage.getItem(KEY) ?? '0') || 0
  } catch {
    // Storage blocked. decideAuthFailure's authError check still stops a
    // loop: a failed renewal always comes back with ?authError=.
  }
  if (Date.now() - last < MIN_GAP_MS) return false

  const go = () => {
    try {
      sessionStorage.setItem(KEY, String(Date.now()))
    } catch {
      // As above.
    }
    window.location.assign(`${loginUrl}?return=${encodeURIComponent(returnPath(location.pathname))}`)
  }

  if (document.visibilityState === 'visible') {
    go()
  } else {
    waiting = true
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return
      document.removeEventListener('visibilitychange', onVisible)
      waiting = false
      go()
    }
    document.addEventListener('visibilitychange', onVisible)
  }
  return true
}

/** What the page does about a failed request:
 *  - `other`: not a 401, the caller reports it as an error;
 *  - `pending`: a renewal is already under way, nothing to do;
 *  - `renew`: pass through GitHub now (or when the tab is visible);
 *  - `stuck`: the session needs renewing but going again would loop, so show
 *    a way out (renew by hand, or sign out);
 *  - `gate`: the session is gone, go back to the sign-in screen. */
export type AuthFailureAction = 'other' | 'pending' | 'renew' | 'stuck' | 'gate'

export function decideAuthFailure(
  err: { status?: number; reauth?: boolean } | null,
  state: { pending: boolean; loginUrl?: string; authError: string },
  renew: (loginUrl: string) => boolean = reauthenticate,
): AuthFailureAction {
  if (!err || err.status !== 401) return 'other'
  if (state.pending) return 'pending'
  if (!err.reauth || !state.loginUrl) return 'gate'
  // This load came back from GitHub with an error: going straight back would
  // repeat it, whatever the loop guard's storage says.
  if (state.authError) return 'stuck'
  return renew(state.loginUrl) ? 'renew' : 'stuck'
}
