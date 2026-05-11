import { useParams, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, TrendingUp, TrendingDown } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import { api } from '../api/client'
import { AlertBadge } from '../components/AlertBadge'
import { EvidenceCard } from '../components/EvidenceCard'

export function ReportDetail() {
  const { id, reportId } = useParams()
  const thesisId = Number(id)
  const rId      = Number(reportId)

  const { data: report, isLoading } = useQuery({
    queryKey: ['report', thesisId, rId],
    queryFn: () => api.getReport(thesisId, rId),
    enabled: !isNaN(thesisId) && !isNaN(rId),
  })

  const { data: thesis } = useQuery({
    queryKey: ['thesis', thesisId],
    queryFn: () => api.getThesis(thesisId),
    enabled: !isNaN(thesisId),
  })

  if (isLoading) return <div className="p-6 text-[#6b7a99] text-sm">Loading…</div>
  if (!report)   return <div className="p-6 text-[#6b7a99] text-sm">Report not found</div>

  const scoreDelta = report.thesisScoreDelta

  return (
    <div className="h-full overflow-y-auto"><div className="p-6 max-w-6xl">
      <Link to={`/thesis/${id}`} className="flex items-center gap-1 text-sm text-[#6b7a99] hover:text-[#e8eaf0] mb-5 transition-colors w-fit">
        <ArrowLeft size={14} />
        {thesis ? `${thesis.symbol} — ${thesis.companyName}` : `Thesis #${id}`}
      </Link>

      <div className="flex items-start justify-between gap-6 mb-6">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-3 mb-2 flex-wrap">
            <h1 className="text-xl font-bold text-[#e8eaf0]">{report.title}</h1>
            <AlertBadge level={report.alertLevel} />
          </div>
          <p className="text-sm text-[#9ca3af] leading-relaxed">{report.summary}</p>
        </div>
        <div className="text-right shrink-0">
          <div className="text-xs text-[#6b7a99] mb-1.5">Score Change</div>
          <div className="flex items-center gap-2 justify-end">
            <span className="text-lg font-mono text-[#9ca3af]">{(report.thesisScoreBefore * 100).toFixed(0)}</span>
            <span className="text-[#3d4a63]">→</span>
            <span className="text-lg font-mono font-bold text-[#e8eaf0]">{(report.thesisScoreAfter * 100).toFixed(0)}</span>
            {scoreDelta !== 0 && (
              <span className={`flex items-center gap-0.5 text-sm font-semibold ${scoreDelta > 0 ? 'text-green-400' : 'text-red-400'}`}>
                {scoreDelta > 0 ? <TrendingUp size={14} /> : <TrendingDown size={14} />}
                {scoreDelta > 0 ? '+' : ''}{(scoreDelta * 100).toFixed(0)}
              </span>
            )}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[1fr_380px] gap-6">
        <div className="rounded-2xl border border-[#2a3245] bg-[#0f1117] p-6 min-w-0">
          <h2 className="text-xs font-semibold text-[#6b7a99] uppercase tracking-wider mb-5">Report</h2>
          <div className="markdown-body">
            <ReactMarkdown
              components={{
                h2: ({ children }) => <h2 className="text-lg font-bold text-[#e8eaf0] mt-6 mb-3 first:mt-0">{children}</h2>,
                h3: ({ children }) => <h3 className="text-sm font-semibold text-[#9ca3af] mt-5 mb-2 uppercase tracking-wide">{children}</h3>,
                h4: ({ children }) => <h4 className="text-sm font-medium text-[#9ca3af] mt-4 mb-2">{children}</h4>,
                p: ({ children }) => <p className="text-sm text-[#c8ccd8] leading-relaxed mb-3">{children}</p>,
                ul: ({ children }) => <ul className="space-y-1 mb-3 pl-4">{children}</ul>,
                ol: ({ children }) => <ol className="space-y-2 mb-3 pl-4 list-decimal">{children}</ol>,
                li: ({ children }) => <li className="text-sm text-[#c8ccd8] leading-relaxed">{children}</li>,
                strong: ({ children }) => <strong className="font-semibold text-[#e8eaf0]">{children}</strong>,
                em: ({ children }) => <em className="text-[#9ca3af] not-italic">{children}</em>,
                blockquote: ({ children }) => (
                  <blockquote className="border-l-2 border-[#2a3245] pl-4 my-3 text-[#9ca3af] italic text-sm leading-relaxed">
                    {children}
                  </blockquote>
                ),
                a: ({ href, children }) => (
                  <a href={href} target="_blank" rel="noreferrer" className="text-blue-400 hover:text-blue-300 underline underline-offset-2 transition-colors">
                    {children}
                  </a>
                ),
                hr: () => <hr className="border-[#1e2435] my-5" />,
                code: ({ children }) => <code className="text-xs font-mono bg-[#1e2435] text-[#9ca3af] px-1.5 py-0.5 rounded">{children}</code>,
              }}
            >
              {report.markdownReport}
            </ReactMarkdown>
          </div>
        </div>

        <div>
          <h2 className="text-xs font-semibold text-[#6b7a99] uppercase tracking-wider mb-4">
            Evidence ({report.snippets.length})
          </h2>
          <div className="space-y-2">
            {report.snippets.map(s => <EvidenceCard key={s.id} s={s} />)}
          </div>
        </div>
      </div>
    </div>
    </div>
  )
}
