// ------------------------------------------------------------
// Auth
// ------------------------------------------------------------

export interface AuthUser {
  id: number
  email: string
  name: string
}

export interface AuthResponse {
  token: string
  user: AuthUser
}

const TOKEN_KEY = 'at_token'

export const authStorage = {
  getToken: () => localStorage.getItem(TOKEN_KEY),
  setToken: (t: string) => localStorage.setItem(TOKEN_KEY, t),
  clear: () => localStorage.removeItem(TOKEN_KEY),
}

export type AlertLevel = 'high' | 'medium' | 'low' | 'none'
export type Direction = 'bullish' | 'bearish' | 'neutral'
export type Stance = 'support' | 'contradict' | 'neutral'
export type Market = 'us' | 'cn'

export interface ThesisItem {
  id: number
  symbol: string
  companyName: string
  market: Market
  direction: Direction
  coreClaim: string
  confidenceScore: number
  alertLevel: AlertLevel
  status: string
  createdAt: string
  updatedAt: string
}

export interface AssumptionItem {
  id: number
  key: string
  text: string
  type: string
  importance: number
  currentScore: number
  posCount: number
  negCount: number
  neutralCount: number
}

export interface ThesisDetailResp extends ThesisItem {
  assumptions: AssumptionItem[]
}

export interface ScorePoint {
  date: string
  score: number
}

export interface ReportItem {
  id: number
  runDate: string
  title: string
  summary: string
  thesisScoreBefore: number
  thesisScoreAfter: number
  thesisScoreDelta: number
  alertLevel: AlertLevel
  snippetCount: number
}

export interface SnippetItem {
  id: number
  assumptionId: number
  candidateSource: string
  candidateUrl: string
  candidateTitle: string
  snippetText: string
  stance: Stance
  impact: number
  confidence: number
  publishedAt: string | null
}

export interface MarketContext {
  symbol: string
  stock_open: number
  stock_close: number
  stock_return: number
  market_etf: string
  market_open: number
  market_close: number
  market_return: number
  sector_etf?: string
  sector_return?: number
  relative_return: number
  alert_level: string
}

export interface ReportDetailResp extends ReportItem {
  markdownReport: string
  marketContext: MarketContext | null
  snippets: SnippetItem[]
}

export interface SSEEvent {
  kind: 'section' | 'pending' | 'ok' | 'error' | 'done'
  text: string
  id?: number
}

// ------------------------------------------------------------
// Fetch helpers
// ------------------------------------------------------------

function authHeaders(): HeadersInit {
  const token = authStorage.getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: authHeaders() })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error((body as { error?: string }).error ?? `HTTP ${res.status}`)
  }
  return res.json()
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as { error?: string }).error ?? `HTTP ${res.status}`)
  return data as T
}

// ------------------------------------------------------------
// API functions (used with React Query queryFn)
// ------------------------------------------------------------

export const api = {
  register: (email: string, password: string, name?: string) =>
    post<AuthResponse>('/api/auth/register', { email, password, name }),

  login: (email: string, password: string) =>
    post<AuthResponse>('/api/auth/login', { email, password }),

  logout: () =>
    post<void>('/api/auth/logout', {}),

  me: () =>
    get<AuthUser>('/api/me'),

  listTheses: () =>
    get<ThesisItem[]>('/api/theses'),

  getThesis: (id: number) =>
    get<ThesisDetailResp>(`/api/theses/${id}`),

  getScoreHistory: (id: number) =>
    get<ScorePoint[]>(`/api/theses/${id}/score-history`),

  listReports: (id: number) =>
    get<ReportItem[]>(`/api/theses/${id}/reports`),

  getReport: (thesisId: number, reportId: number) =>
    get<ReportDetailResp>(`/api/theses/${thesisId}/reports/${reportId}`),

  listEvidence: (thesisId: number, filter?: 'event' | 'news') => {
    const qs = filter ? `?filter=${filter}` : ''
    return get<SnippetItem[]>(`/api/theses/${thesisId}/evidence${qs}`)
  },

  triggerRun: (thesisId: number) =>
    post<{ jobId: number; status: string }>(`/api/theses/${thesisId}/run`, {}),

  deleteThesis: (thesisId: number) =>
    fetch(`/api/theses/${thesisId}`, { method: 'DELETE', headers: authHeaders() }).then(async r => {
      if (!r.ok) {
        const body = await r.json().catch(() => ({}))
        throw new Error((body as { error?: string }).error ?? `HTTP ${r.status}`)
      }
      return r.json()
    }),
}

// ------------------------------------------------------------
// Streaming parse (SSE over fetch, so we can POST a body)
// ------------------------------------------------------------

export function parseThesisStream(
  text: string,
  onEvent: (ev: SSEEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  return (async () => {
    let res: Response
    try {
      res = await fetch('/api/theses/parse-stream', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify({ text }),
        signal,
      })
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      throw new Error(`stream open failed: ${message}`)
    }

    if (!res.ok || !res.body) {
      throw new Error(`HTTP ${res.status}`)
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buf = ''
    let eventCount = 0
    let lastEvent = ''

    while (true) {
      let chunk: ReadableStreamReadResult<Uint8Array>
      try {
        chunk = await reader.read()
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        throw new Error(`stream read failed after ${eventCount} events${lastEvent ? `; last event: ${lastEvent}` : ''}; ${message}`)
      }

      const { done, value } = chunk
      if (done) break
      buf += decoder.decode(value, { stream: true })
      const lines = buf.split('\n')
      buf = lines.pop() ?? ''
      for (const line of lines) {
        if (line.startsWith('data: ')) {
          try {
            const ev = JSON.parse(line.slice(6)) as SSEEvent
            eventCount += 1
            lastEvent = `${ev.kind}: ${ev.text}`
            onEvent(ev)
          } catch { /* ignore malformed lines */ }
        }
      }
    }
  })()
}
