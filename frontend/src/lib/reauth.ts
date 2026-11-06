const KEY = 'yana.reauthAt'
const MIN_GAP_MS = 60_000
/** Mirrors the backend's rule for `return`; the backend still checks it. */
const RETURN_PATH = /^\/[A-Za-z0-9/_-]*$/

let waiting = false

/** Sends the browser through GitHub to renew its token and back to the current
 *  path. Waits for the tab to be visible, and refuses to go twice within a
 *  minute so a failing round-trip cannot loop. Returns false when it refused,
 *  so the caller shows the sign-in screen instead. */
export function reauthenticate(loginUrl: string): boolean {
  if (waiting) return true
  let last = 0
  try {
    last = Number(sessionStorage.getItem(KEY) ?? '0') || 0
  } catch {
    // Storage blocked: every call gets its one try.
  }
  if (Date.now() - last < MIN_GAP_MS) return false

  const go = () => {
    try {
      sessionStorage.setItem(KEY, String(Date.now()))
    } catch {
      // As above.
    }
    const path = location.pathname
    const ret = RETURN_PATH.test(path) ? path : '/'
    window.location.assign(`${loginUrl}?return=${encodeURIComponent(ret)}`)
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
