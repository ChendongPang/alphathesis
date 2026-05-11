import { ExternalLink } from 'lucide-react'
import type { SnippetItem } from '../api/client'

const stanceStyle: Record<string, string> = {
  support:    'border-l-green-500 bg-green-500/5',
  contradict: 'border-l-red-500 bg-red-500/5',
  neutral:    'border-l-slate-600 bg-slate-800/30',
}

const impactColor = (v: number) => v > 0 ? 'text-green-400' : v < 0 ? 'text-red-400' : 'text-slate-500'
const sourceLabel = (s: string) => s === 'cn_official_cninfo' ? '公告' : s.includes('news') ? '新闻' : 'SEC'

export function EvidenceCard({ s }: { s: SnippetItem }) {
  return (
    <div className={`border-l-2 rounded-r-xl px-4 py-3 ${stanceStyle[s.stance] ?? stanceStyle.neutral}`}>
      <div className="flex items-start justify-between gap-2 mb-1">
        <a
          href={s.candidateUrl}
          target="_blank"
          rel="noreferrer"
          className="text-sm text-blue-400 hover:text-blue-300 flex items-center gap-1 leading-snug"
        >
          {s.candidateTitle}
          <ExternalLink size={11} className="shrink-0 mt-0.5" />
        </a>
        <div className="flex items-center gap-2 shrink-0">
          <span className="text-xs px-1.5 py-0.5 rounded bg-[#1e2435] text-[#6b7a99]">{sourceLabel(s.candidateSource)}</span>
          <span className={`text-xs font-mono font-semibold tabular-nums ${impactColor(s.impact)}`}>
            {s.impact > 0 ? '+' : ''}{s.impact.toFixed(2)}
          </span>
        </div>
      </div>
      <p className="text-xs text-[#9ca3af] leading-relaxed">"{s.snippetText}"</p>
      <div className="mt-1.5 flex items-center gap-2 text-xs text-[#6b7a99]">
        <span>{s.publishedAt ? s.publishedAt.slice(0, 10) : '—'}</span>
        <span>·</span>
        <span>conf {(s.confidence * 100).toFixed(0)}%</span>
      </div>
    </div>
  )
}
