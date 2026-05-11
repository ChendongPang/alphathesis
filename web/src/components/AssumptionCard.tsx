import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import type { AssumptionItem } from '../api/client'

export function AssumptionCard({ a }: { a: AssumptionItem }) {
  const [open, setOpen] = useState(false)
  const pct = Math.round(a.currentScore * 100)
  const color = a.currentScore >= 0.65 ? '#22c55e' : a.currentScore >= 0.45 ? '#f59e0b' : '#ef4444'

  return (
    <div className="rounded-xl border border-[#2a3245] bg-[#161b27] overflow-hidden">
      <button
        className="w-full px-4 py-3 flex items-start gap-3 hover:bg-[#1e2435] transition-colors text-left"
        onClick={() => setOpen(o => !o)}
      >
        <div className="mt-0.5 text-[#6b7a99]">
          {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center justify-between gap-3 mb-2">
            <span className="text-xs font-mono text-[#6b7a99]">{a.key}</span>
            <span className="text-sm font-bold tabular-nums" style={{ color }}>{pct}</span>
          </div>
          <div className="h-1.5 rounded-full bg-[#1e2435] overflow-hidden">
            <div className="h-full rounded-full transition-all duration-500" style={{ width: `${pct}%`, background: color }} />
          </div>
          {open && (
            <p className="mt-3 text-sm text-[#e8eaf0] leading-relaxed">{a.text}</p>
          )}
        </div>
        <div className="flex gap-2 text-xs shrink-0">
          <span className="text-green-400">+{a.posCount}</span>
          <span className="text-red-400">−{a.negCount}</span>
          <span className="text-slate-500">~{a.neutralCount}</span>
        </div>
      </button>
    </div>
  )
}
