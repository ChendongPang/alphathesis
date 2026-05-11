import { useState } from 'react'
import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, TrendingUp, TrendingDown, Trash2 } from 'lucide-react'
import { api } from '../api/client'
import { ScoreGauge } from '../components/ScoreGauge'
import { AlertBadge } from '../components/AlertBadge'
import { ScoreChart } from '../components/ScoreChart'
import { AssumptionCard } from '../components/AssumptionCard'
import { EvidenceCard } from '../components/EvidenceCard'

type Tab = 'overview' | 'evidence' | 'reports'

export function ThesisDetail() {
  const { id } = useParams()
  const thesisId = Number(id)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState<Tab>('overview')
  const [deleting, setDeleting] = useState(false)

  const handleDelete = async () => {
    if (!window.confirm('确定要删除这个论题吗？此操作不可恢复。')) return
    setDeleting(true)
    try {
      await api.deleteThesis(thesisId)
      queryClient.invalidateQueries({ queryKey: ['theses'] })
      navigate('/')
    } catch (e) {
      alert('删除失败: ' + (e instanceof Error ? e.message : String(e)))
      setDeleting(false)
    }
  }

  const { data: thesis, isLoading } = useQuery({
    queryKey: ['thesis', thesisId],
    queryFn: () => api.getThesis(thesisId),
    enabled: !isNaN(thesisId),
  })

  const { data: scoreHistory = [] } = useQuery({
    queryKey: ['score-history', thesisId],
    queryFn: () => api.getScoreHistory(thesisId),
    enabled: !isNaN(thesisId),
  })

  const { data: reports = [] } = useQuery({
    queryKey: ['reports', thesisId],
    queryFn: () => api.listReports(thesisId),
    enabled: !isNaN(thesisId),
  })

  const { data: snippets = [] } = useQuery({
    queryKey: ['evidence', thesisId],
    queryFn: () => api.listEvidence(thesisId),
    enabled: !isNaN(thesisId),
  })

  if (isLoading) return <div className="p-6 text-[#6b7a99] text-sm">Loading…</div>
  if (!thesis)   return <div className="p-6 text-[#6b7a99] text-sm">Thesis not found</div>

  const tabs: { key: Tab; label: string; count?: number }[] = [
    { key: 'overview',  label: 'Overview' },
    { key: 'reports',   label: 'Reports',  count: reports.length },
    { key: 'evidence',  label: 'Evidence', count: snippets.length },
  ]

  return (
    <div className="h-full overflow-y-auto"><div className="p-6 max-w-5xl">
      <div className="flex items-center justify-between mb-5">
        <Link to="/" className="flex items-center gap-1 text-sm text-[#6b7a99] hover:text-[#e8eaf0] transition-colors">
          <ArrowLeft size={14} />
          All Theses
        </Link>
        <button
          onClick={handleDelete}
          disabled={deleting}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs text-red-400 hover:text-red-300 hover:bg-red-500/10 border border-transparent hover:border-red-500/20 disabled:opacity-40 transition-all"
        >
          <Trash2 size={13} />
          {deleting ? '删除中...' : '删除论题'}
        </button>
      </div>

      <div className="flex items-start gap-5 mb-6">
        <ScoreGauge score={thesis.confidenceScore} size={104} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-3 mb-1 flex-wrap">
            <span className="text-3xl font-bold text-[#e8eaf0]">{thesis.symbol}</span>
            <span className={`text-sm px-2.5 py-0.5 rounded-full font-medium ${thesis.direction === 'bullish' ? 'bg-green-500/15 text-green-400' : 'bg-red-500/15 text-red-400'}`}>
              {thesis.direction === 'bullish' ? '↑ BULLISH' : '↓ BEARISH'}
            </span>
            <AlertBadge level={thesis.alertLevel} />
          </div>
          <p className="text-base text-[#9ca3af]">{thesis.companyName}</p>
          <p className="text-sm text-[#6b7a99] mt-1 leading-relaxed">{thesis.coreClaim}</p>
        </div>
      </div>

      <div className="flex gap-1 border-b border-[#1e2435] mb-6">
        {tabs.map(t => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={`flex items-center gap-1.5 px-4 py-2.5 text-sm font-medium transition-colors border-b-2 -mb-px ${tab === t.key ? 'border-blue-500 text-blue-400' : 'border-transparent text-[#6b7a99] hover:text-[#9ca3af]'}`}
          >
            {t.label}
            {t.count !== undefined && t.count > 0 && (
              <span className="text-xs px-1.5 py-0.5 rounded-full bg-[#1e2435] text-[#6b7a99]">{t.count}</span>
            )}
          </button>
        ))}
      </div>

      {tab === 'overview' && (
        <div className="space-y-6">
          <div className="rounded-2xl border border-[#2a3245] bg-[#0f1117] p-5">
            <h3 className="text-xs font-semibold text-[#6b7a99] uppercase tracking-wider mb-4">Score History</h3>
            <ScoreChart data={scoreHistory} />
          </div>
          <div>
            <h3 className="text-xs font-semibold text-[#6b7a99] uppercase tracking-wider mb-3">
              Assumptions ({thesis.assumptions.length})
            </h3>
            <div className="space-y-2">
              {thesis.assumptions.map(a => <AssumptionCard key={a.id} a={a} />)}
            </div>
          </div>
        </div>
      )}

      {tab === 'reports' && (
        <div className="space-y-2">
          {reports.length === 0 ? (
            <p className="text-[#6b7a99] text-sm">No reports yet.</p>
          ) : reports.map(r => {
            const delta = r.thesisScoreDelta
            return (
              <Link
                key={r.id}
                to={`/thesis/${thesis.id}/report/${r.id}`}
                className="block rounded-xl border border-[#2a3245] bg-[#0f1117] hover:border-[#3b4a6b] hover:bg-[#131720] transition-all px-5 py-4"
              >
                <div className="flex items-center justify-between gap-4">
                  <div className="flex items-center gap-3 min-w-0">
                    <span className="text-sm font-mono text-[#6b7a99] shrink-0">{r.runDate}</span>
                    <AlertBadge level={r.alertLevel} />
                    <p className="text-sm text-[#9ca3af] truncate">{r.summary}</p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <span className="text-sm font-mono text-[#6b7a99]">{(r.thesisScoreBefore * 100).toFixed(0)}</span>
                    <span className="text-[#3d4a63]">→</span>
                    <span className="text-sm font-mono font-bold text-[#e8eaf0]">{(r.thesisScoreAfter * 100).toFixed(0)}</span>
                    <span className={`flex items-center gap-0.5 text-xs font-semibold min-w-[40px] justify-end ${delta > 0 ? 'text-green-400' : delta < 0 ? 'text-red-400' : 'text-[#6b7a99]'}`}>
                      {delta > 0 ? <TrendingUp size={12} /> : delta < 0 ? <TrendingDown size={12} /> : null}
                      {delta > 0 ? '+' : ''}{delta !== 0 ? (delta * 100).toFixed(0) : '—'}
                    </span>
                  </div>
                </div>
                <p className="mt-1.5 text-xs text-[#6b7a99]">
                  {r.snippetCount} snippet{r.snippetCount !== 1 ? 's' : ''}
                </p>
              </Link>
            )
          })}
        </div>
      )}

      {tab === 'evidence' && (
        <div>
          {snippets.length === 0 ? (
            <p className="text-[#6b7a99] text-sm">No evidence snippets yet.</p>
          ) : (
            <div className="space-y-2">
              {snippets.map(s => <EvidenceCard key={s.id} s={s} />)}
            </div>
          )}
        </div>
      )}
    </div>
    </div>
  )
}
