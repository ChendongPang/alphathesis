import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { TrendingUp, TrendingDown, Minus, ExternalLink, Loader2 } from 'lucide-react'
import { api } from '../api/client'
import type { SnippetItem, ThesisDetailResp, MarketContext } from '../api/client'
import { AlertBadge } from '../components/AlertBadge'
import { ScoreGauge } from '../components/ScoreGauge'
import { ScoreChart } from '../components/ScoreChart'
import { useAnalysisSession } from '../state/analysisSession'

type ETab = 'all' | 'event' | 'news' | 'market'

const isEvent = (s: SnippetItem) =>
  s.candidateSource === 'cn_official_cninfo' || s.candidateSource === 'us_sec'
const isNews = (s: SnippetItem) => s.candidateSource.includes('news')

const srcLabel = (s: string) =>
  s === 'cn_official_cninfo' ? '公告' : s === 'us_sec' ? 'SEC' : '新闻'

const stanceCls: Record<string, string> = {
  support:    'border-l-green-500 bg-green-500/5',
  contradict: 'border-l-red-500 bg-red-500/5',
  neutral:    'border-l-slate-600 bg-slate-800/30',
}

const impactCls = (v: number) =>
  v > 0 ? 'text-green-400' : v < 0 ? 'text-red-400' : 'text-slate-500'

export function Dashboard() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const analysis = useAnalysisSession()
  const [selId, setSelId] = useState<number | null>(null)
  const [eTab, setETab]   = useState<ETab>('all')

  const { data: theses = [] } = useQuery({
    queryKey: ['theses'],
    queryFn: api.listTheses,
  })

  const activeId = selId ?? theses[0]?.id ?? null

  const { data: thesis } = useQuery({
    queryKey: ['thesis', activeId],
    queryFn: () => api.getThesis(activeId!),
    enabled: activeId != null,
  })

  const { data: scoreHistory = [] } = useQuery({
    queryKey: ['score-history', activeId],
    queryFn: () => api.getScoreHistory(activeId!),
    enabled: activeId != null,
  })

  const { data: snippets = [] } = useQuery({
    queryKey: ['evidence', activeId],
    queryFn: () => api.listEvidence(activeId!),
    enabled: activeId != null,
  })

  const { data: reports = [] } = useQuery({
    queryKey: ['reports', activeId],
    queryFn: () => api.listReports(activeId!),
    enabled: activeId != null,
  })

  const snippetsFor = (tab: ETab): SnippetItem[] => {
    if (tab === 'event') return snippets.filter(isEvent)
    if (tab === 'news')  return snippets.filter(isNews)
    return snippets
  }

  const eTabs = [
    { key: 'all'    as const, label: '全部', count: snippets.length },
    { key: 'event'  as const, label: '事件', count: snippets.filter(isEvent).length },
    { key: 'news'   as const, label: '新闻', count: snippets.filter(isNews).length },
    { key: 'market' as const, label: '行情', count: undefined },
  ]

  const alertLevel = thesis?.alertLevel ?? 'none'

  useEffect(() => {
    if (analysis.thesisId || analysis.done) {
      queryClient.invalidateQueries({ queryKey: ['theses'] })
      if (analysis.thesisId) {
        queryClient.invalidateQueries({ queryKey: ['thesis', analysis.thesisId] })
        queryClient.invalidateQueries({ queryKey: ['reports', analysis.thesisId] })
      }
    }
  }, [analysis.done, analysis.thesisId, queryClient])

  return (
    <div className="h-full flex overflow-hidden">

      {/* ── 1. Thesis List ──────────────────────────────────────── */}
      <div
        className="w-56 shrink-0 overflow-y-auto p-3 space-y-1.5"
        style={{ borderRight: '1px solid #1e2435' }}
      >
        <p className="text-xs font-semibold uppercase tracking-wider px-2 mb-3" style={{ color: '#3d4a63' }}>
          Theses
        </p>
        {analysis.active && (
          <button
            onClick={() => navigate('/submit')}
            className="w-full text-left rounded-xl px-3 py-3 transition-all bg-blue-600/10 border border-blue-500/30 hover:bg-blue-600/15"
          >
            <div className="flex items-center justify-between mb-1">
              <span className="text-sm font-bold text-blue-300">
                {analysis.thesisId ? `Thesis #${analysis.thesisId}` : 'Analyzing'}
              </span>
              <Loader2 size={15} className="text-blue-400 animate-spin" />
            </div>
            <p className="text-xs text-[#6b7a99] mb-2 line-clamp-2">{analysis.text}</p>
            <span className="text-xs text-blue-400">正在分析，点击查看进度</span>
          </button>
        )}
        {theses.length === 0 && !analysis.active && (
          <p className="text-xs text-[#3d4a63] px-2">暂无论题</p>
        )}
        {theses.map(t => {
          const sel = t.id === activeId
          const pct = Math.round(t.confidenceScore * 100)
          const sc  = pct >= 65 ? '#22c55e' : pct >= 45 ? '#f59e0b' : '#ef4444'
          return (
            <button
              key={t.id}
              onClick={() => { setSelId(t.id); setETab('all') }}
              className={`w-full text-left rounded-xl px-3 py-3 transition-all ${sel ? 'bg-[#1a2035] border border-[#3b4a6b]' : 'hover:bg-[#161b27] border border-transparent'}`}
            >
              <div className="flex items-center justify-between mb-1">
                <span className="text-sm font-bold text-[#e8eaf0]">{t.symbol}</span>
                <span className="text-sm font-bold tabular-nums" style={{ color: sc }}>{pct}</span>
              </div>
              <p className="text-xs text-[#6b7a99] mb-2 truncate">{t.companyName}</p>
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-1.5">
                  <span className={`text-xs px-1.5 rounded-full font-medium ${t.direction === 'bullish' ? 'bg-green-500/15 text-green-400' : 'bg-red-500/15 text-red-400'}`}>
                    {t.direction === 'bullish' ? '↑' : '↓'}
                  </span>
                  <AlertBadge level={t.alertLevel} />
                </div>
              </div>
            </button>
          )
        })}
      </div>

      {/* ── 2. Assumptions + Evidence Feed ─────────────────────── */}
      <div className="flex-1 flex flex-col overflow-hidden" style={{ borderRight: '1px solid #1e2435' }}>

        {/* Assumptions */}
        <div className="shrink-0 px-5 py-4" style={{ borderBottom: '1px solid #1e2435' }}>
          <p className="text-xs font-semibold uppercase tracking-wider mb-3" style={{ color: '#3d4a63' }}>
            Assumptions
          </p>
          {!thesis && (
            <p className="text-xs text-[#3d4a63]">选择一个论题</p>
          )}
          <div className="space-y-2.5">
            {thesis?.assumptions.map(a => {
              const pct = Math.round(a.currentScore * 100)
              const col = pct >= 65 ? '#22c55e' : pct >= 45 ? '#f59e0b' : '#ef4444'
              const net = a.posCount - a.negCount
              const sig = net >= 2 ? 'up' : net <= -2 ? 'down' : 'flat'
              return (
                <div key={a.id} className="flex items-start gap-3 min-w-0">
                  <span className={`shrink-0 mt-0.5 ${sig === 'up' ? 'text-green-400' : sig === 'down' ? 'text-red-400' : 'text-[#6b7a99]'}`}>
                    {sig === 'up' ? <TrendingUp size={13} /> : sig === 'down' ? <TrendingDown size={13} /> : <Minus size={13} />}
                  </span>
                  <p className="flex-1 text-sm text-[#c8ccd8] leading-snug min-w-0">{a.text}</p>
                  <div className="shrink-0 flex items-center gap-2 mt-0.5">
                    <span className="text-xs font-bold tabular-nums" style={{ color: col }}>{pct}</span>
                    <span className="text-xs text-green-400 tabular-nums">+{a.posCount}</span>
                    <span className="text-xs text-red-400 tabular-nums">−{a.negCount}</span>
                    <span className={`text-xs font-semibold w-8 ${sig === 'up' ? 'text-green-400' : sig === 'down' ? 'text-red-400' : 'text-[#6b7a99]'}`}>
                      {sig === 'up' ? '增强' : sig === 'down' ? '削弱' : '持平'}
                    </span>
                  </div>
                </div>
              )
            })}
          </div>
        </div>

        {/* Evidence Feed */}
        <div className="flex-1 flex flex-col overflow-hidden">
          <div className="shrink-0 flex gap-0.5 px-5 pt-2" style={{ borderBottom: '1px solid #1e2435' }}>
            {eTabs.map(t => (
              <button
                key={t.key}
                onClick={() => setETab(t.key)}
                className={`flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-colors ${eTab === t.key ? 'border-blue-500 text-blue-400' : 'border-transparent text-[#6b7a99] hover:text-[#9ca3af]'}`}
              >
                {t.label}
                {t.count !== undefined && t.count > 0 && (
                  <span className="px-1.5 py-0.5 rounded-full bg-[#1e2435] text-[#6b7a99]">{t.count}</span>
                )}
              </button>
            ))}
          </div>

          <div className="flex-1 overflow-y-auto p-4 space-y-2">
            {eTab === 'market' ? (
              <MarketPanel thesis={thesis ?? null} thesisId={activeId} />
            ) : snippetsFor(eTab).length === 0 ? (
              <p className="text-sm text-[#6b7a99] mt-2">暂无证据</p>
            ) : snippetsFor(eTab).map(s => (
              <div key={s.id} className={`border-l-2 rounded-r-xl px-3 py-2.5 ${stanceCls[s.stance] ?? stanceCls.neutral}`}>
                <div className="flex items-start justify-between gap-2 mb-1">
                  <a
                    href={s.candidateUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-xs text-blue-400 hover:text-blue-300 flex items-center gap-1 leading-snug min-w-0"
                  >
                    <span className="truncate">{s.candidateTitle}</span>
                    <ExternalLink size={10} className="shrink-0" />
                  </a>
                  <div className="flex items-center gap-1.5 shrink-0">
                    <span className="text-xs px-1.5 py-0.5 rounded bg-[#1e2435] text-[#6b7a99]">
                      {srcLabel(s.candidateSource)}
                    </span>
                    <span className={`text-xs font-mono font-semibold tabular-nums ${impactCls(s.impact)}`}>
                      {s.impact > 0 ? '+' : ''}{s.impact.toFixed(2)}
                    </span>
                  </div>
                </div>
                <p className="text-xs text-[#9ca3af] leading-relaxed mb-1.5">"{s.snippetText}"</p>
                <div className="flex items-center gap-2 text-xs" style={{ color: '#3d4a63' }}>
                  <span>{s.publishedAt ? s.publishedAt.slice(0, 10) : '—'}</span>
                  <span>·</span>
                  <span>conf {(s.confidence * 100).toFixed(0)}%</span>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* ── 3. Score + Stats ────────────────────────────────────── */}
      <div className="w-72 shrink-0 overflow-y-auto p-4 space-y-4">
        {thesis && (
          <>
            <div className="flex items-center gap-3">
              <ScoreGauge score={thesis.confidenceScore} size={88} />
              <div>
                <p className="text-lg font-bold text-[#e8eaf0]">{thesis.symbol}</p>
                <p className={`text-xs font-medium mt-0.5 ${thesis.direction === 'bullish' ? 'text-green-400' : 'text-red-400'}`}>
                  {thesis.direction === 'bullish' ? '↑ BULLISH' : '↓ BEARISH'}
                </p>
                <div className="mt-1.5"><AlertBadge level={alertLevel} /></div>
              </div>
            </div>

            <div className="rounded-xl border border-[#2a3245] bg-[#0f1117] p-3">
              <p className="text-xs font-semibold uppercase tracking-wider mb-2" style={{ color: '#6b7a99' }}>
                Score History
              </p>
              <ScoreChart data={scoreHistory} />
            </div>

            {reports.length > 0 && (
              <div className="rounded-xl border border-[#2a3245] bg-[#0f1117] p-3">
                <p className="text-xs font-semibold uppercase tracking-wider mb-2" style={{ color: '#6b7a99' }}>
                  Reports
                </p>
                <div className="space-y-0.5">
                  {reports.slice(0, 4).map(r => {
                    const d = r.thesisScoreDelta
                    return (
                      <Link
                        key={r.id}
                        to={`/thesis/${thesis.id}/report/${r.id}`}
                        className="flex items-center justify-between rounded-lg px-2 py-1.5 hover:bg-[#161b27] transition-colors group"
                      >
                        <span className="text-xs font-mono text-[#6b7a99] group-hover:text-[#9ca3af]">{r.runDate}</span>
                        <div className="flex items-center gap-2">
                          <AlertBadge level={r.alertLevel} />
                          <span className={`text-xs font-semibold tabular-nums min-w-[28px] text-right ${d > 0 ? 'text-green-400' : d < 0 ? 'text-red-400' : 'text-[#6b7a99]'}`}>
                            {d > 0 ? '+' : ''}{d !== 0 ? (d * 100).toFixed(0) : '±0'}
                          </span>
                        </div>
                      </Link>
                    )
                  })}
                </div>
                <Link
                  to={`/thesis/${thesis.id}`}
                  className="block text-xs text-center mt-2 pt-2 text-[#6b7a99] hover:text-blue-400 transition-colors"
                  style={{ borderTop: '1px solid #1e2435' }}
                >
                  View thesis details →
                </Link>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

function MarketPanel({ thesis, thesisId }: { thesis: ThesisDetailResp | null; thesisId: number | null }) {
  const { data: reports = [] } = useQuery({
    queryKey: ['reports', thesisId],
    queryFn: () => api.listReports(thesisId!),
    enabled: thesisId != null,
  })

  const latestReportId = reports[0]?.id
  const { data: latestReport } = useQuery({
    queryKey: ['report', thesisId, latestReportId],
    queryFn: () => api.getReport(thesisId!, latestReportId!),
    enabled: thesisId != null && latestReportId != null,
  })

  const mc: MarketContext | null = latestReport?.marketContext ?? null

  if (!thesis) return <p className="text-sm text-[#6b7a99] mt-2">选择一个论题</p>
  if (!mc) return <p className="text-sm text-[#6b7a99] mt-2">暂无行情数据</p>

  const ret   = mc.stock_return
  const bench = mc.market_return
  const alpha = mc.relative_return

  return (
    <div className="space-y-3 max-w-lg">
      <div className="rounded-xl border border-[#2a3245] bg-[#161b27] p-4">
        <p className="text-sm font-semibold text-[#e8eaf0] mb-4">{thesis.symbol} · {thesis.companyName}</p>
        <div className="grid grid-cols-2 gap-3">
          <div className="rounded-lg bg-[#0f1117] px-3 py-2.5">
            <p className="text-xs text-[#6b7a99] mb-1">开盘</p>
            <p className="text-base font-bold text-[#e8eaf0] tabular-nums">
              {thesis.market === 'cn' ? '¥' : '$'}{mc.stock_open.toFixed(2)}
            </p>
          </div>
          <div className="rounded-lg bg-[#0f1117] px-3 py-2.5">
            <p className="text-xs text-[#6b7a99] mb-1">收盘</p>
            <p className="text-base font-bold text-[#e8eaf0] tabular-nums">
              {thesis.market === 'cn' ? '¥' : '$'}{mc.stock_close.toFixed(2)}
            </p>
          </div>
          <div className="rounded-lg bg-[#0f1117] px-3 py-2.5">
            <p className="text-xs text-[#6b7a99] mb-1">今日涨跌</p>
            <p className={`text-lg font-bold tabular-nums ${ret >= 0 ? 'text-green-400' : 'text-red-400'}`}>
              {ret > 0 ? '+' : ''}{(ret * 100).toFixed(2)}%
            </p>
          </div>
          <div className="rounded-lg bg-[#0f1117] px-3 py-2.5">
            <p className="text-xs text-[#6b7a99] mb-1">{mc.market_etf}</p>
            <p className={`text-lg font-bold tabular-nums ${bench >= 0 ? 'text-[#9ca3af]' : 'text-red-400'}`}>
              {bench > 0 ? '+' : ''}{(bench * 100).toFixed(2)}%
            </p>
          </div>
          <div className="rounded-lg bg-[#0f1117] px-3 py-2.5 col-span-2">
            <p className="text-xs text-[#6b7a99] mb-1">超额收益 (Alpha)</p>
            <p className={`text-xl font-bold tabular-nums ${alpha >= 0 ? 'text-blue-400' : 'text-red-400'}`}>
              {alpha > 0 ? '+' : ''}{(alpha * 100).toFixed(2)}%
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}
