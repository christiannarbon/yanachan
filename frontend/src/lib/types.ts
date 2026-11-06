export interface GhCliStatus {
  available: boolean
  authenticated: boolean
  login: string
  host: string
  path: string
  detail?: string
}

export interface SessionView {
  mode: 'gh-cli' | 'oauth' | 'env-token' | string
  login: string
}

export interface AuthStatus {
  /** Which identity model the backend runs. */
  authMode: 'local' | 'server'
  /** Server mode only: where the browser navigates to sign in. */
  loginUrl?: string
  authenticated: boolean
  session: SessionView | null
  ghCli: GhCliStatus
  ghCliAllowed: boolean
  oauthEnabled: boolean
  envTokenAvailable: boolean
  oauthScopes: string
  pendingDevice?: { expiresAt: string; interval: number }
}

export interface DeviceStart {
  userCode: string
  verificationUri: string
  expiresIn: number
  interval: number
}

export interface DevicePoll {
  state: 'pending' | 'slow_down' | 'complete' | 'expired' | 'denied'
  session?: SessionView
}

export interface Settings {
  teams: string[]
  orgs: string[]
  limit: number
  windowHours: number
  onlyActive: boolean
  showUrls: boolean
}

export type EntryStatus = 'reply' | 'new' | 'quiet' | ''

export interface Entry {
  number: number
  title: string
  url: string
  repo: string
  isDraft: boolean
  author: string
  authorIsBot: boolean
  createdAt: string
  updatedAt: string
  checks: '' | 'success' | 'failure' | 'pending'
  reviewDecision: '' | 'approved' | 'changes_requested' | 'review_required'
  active: boolean
  hot: boolean
  status: EntryStatus
  humanActors: string[]
  botActors: string[]
  humanCount: number
  botCount: number
  lastActivityAt: string | null
  touched: boolean
  yourState: '' | 'approved' | 'changes_requested' | 'commented'
  yourLastAt: string | null
  awaiting: '' | 'you' | 'team'
  alsoRequestedFromYou: boolean
}

export interface Section {
  id: string
  title: string
  kind: 'mine' | 'review' | 'team' | 'org'
  ref: string
  entries: Entry[]
  total: number
  active: number
  hot: number
  error?: string
}

export interface WindowView {
  /** Machine-readable, so the frontend can word the window in any language. */
  kind: 'fixed' | 'business-day' | string
  /** The backend's own English wording, used as the fallback. */
  label: string
  hours: number
  cutoff: string
  now: string
}

export interface Board {
  login: string
  authMode: string
  window: WindowView
  sections: Section[]
  generatedAt: string
  onlyActive: boolean
  limit: number
  warning?: string
}

/** One column of the dashboard's activity chart, one local calendar day. */
export interface StatsDay {
  /** YYYY-MM-DD in the server's zone. */
  date: string
  opened: number
  merged: number
  /** Distinct pull requests reviewed that day, not reviews submitted. */
  reviewed: number
}

/** The little a highlight needs to link back to a pull request. */
export interface PrRef {
  repo: string
  number: number
  title: string
  url: string
}

/** The week's superlatives. Each one is absent when the week had none. */
export interface Highlights {
  fastestMerge?: PrRef
  fastestMinutes: number
  biggestMerge?: PrRef
  biggestLines: number
  topRepo: string
  topRepoCount: number
}

export interface StatsWeek {
  days: number
  since: string
  until: string
}

export interface Stats {
  login: string
  week: StatsWeek
  generatedAt: string

  opened: number
  merged: number
  /** Closed without merging. */
  closed: number
  /** Distinct pull requests you reviewed. */
  reviewed: number

  reviewsWritten: number
  approvals: number
  repos: number
  additions: number
  deletions: number
  filesChanged: number

  kcal: number
  activeDays: number
  streak: number

  daily: StatsDay[]
  highlights: Highlights
  warning?: string
}

export interface Suggestions {
  orgs: string[]
  teams: string[]
  warning?: string
}
