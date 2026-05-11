import type { AlertLevel } from '../api/client'

const cfg: Record<AlertLevel, { label: string; cls: string }> = {
  high:   { label: 'HIGH',   cls: 'bg-red-500/20 text-red-400 border border-red-500/30' },
  medium: { label: 'MEDIUM', cls: 'bg-amber-500/20 text-amber-400 border border-amber-500/30' },
  low:    { label: 'LOW',    cls: 'bg-blue-500/20 text-blue-400 border border-blue-500/30' },
  none:   { label: 'NONE',   cls: 'bg-slate-700/40 text-slate-400 border border-slate-600/30' },
}

export function AlertBadge({ level }: { level: AlertLevel }) {
  const { label, cls } = cfg[level] ?? cfg.none
  return (
    <span className={`text-xs font-semibold px-2 py-0.5 rounded-full ${cls}`}>
      {label}
    </span>
  )
}
