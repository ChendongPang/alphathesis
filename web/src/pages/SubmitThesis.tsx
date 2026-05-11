import { useState, useEffect, useRef } from 'react'
import { useNavigate, Link } from 'react-router-dom'
import { ArrowLeft, Sparkles, CheckCircle } from 'lucide-react'
import { analysisSession, useAnalysisSession } from '../state/analysisSession'

const thesisExamples = [
  {
    label: 'A股',
    text: '我看多宁德时代，因为储能需求高增、海外电池产能释放会改善盈利，且锂价回落有利于毛利率修复。',
  },
  {
    label: '美股',
    text: 'NVDA is bullish because hyperscaler AI capex remains strong, Blackwell supply is constrained, and data center margins should offset gaming weakness.',
  },
  {
    label: '港股',
    text: '我看多腾讯控股，因为游戏版号和新品周期改善，广告业务受益于视频号商业化，同时回购能持续提升股东回报。',
  },
]

export function SubmitThesis() {
  const navigate = useNavigate()
  const session = useAnalysisSession()
  const [text, setText] = useState(session.text)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const wasActiveRef = useRef(false)

  const active = session.active || session.steps.length > 0
  const shown = session.steps
  const done = session.done

  const handleSubmit = () => {
    analysisSession.start(text)
  }

  const useExample = (exampleText: string) => {
    setText(exampleText)
    window.requestAnimationFrame(() => {
      textareaRef.current?.focus()
      textareaRef.current?.setSelectionRange(exampleText.length, exampleText.length)
    })
  }

  useEffect(() => {
    if (session.active) {
      wasActiveRef.current = true
    }
    if (wasActiveRef.current && session.done && !session.error) {
      const timer = window.setTimeout(() => navigate('/'), 900)
      return () => window.clearTimeout(timer)
    }
  }, [navigate, session.active, session.done, session.error])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [shown])

  return (
    <div className="h-full overflow-y-auto">
      <div className={`p-6 transition-all duration-300 ${active ? 'max-w-5xl' : 'max-w-xl'}`}>
        <Link to="/" className="flex items-center gap-1 text-sm text-[#6b7a99] hover:text-[#e8eaf0] mb-8 transition-colors w-fit">
          <ArrowLeft size={14} />
          Back
        </Link>

        <h1 className="text-2xl font-bold text-[#e8eaf0] mb-1">New Thesis</h1>
        <p className="text-sm text-[#6b7a99] mb-8">用一句话描述你的投资论点，AI 自动解析</p>

        <div className={`flex gap-5 items-start transition-all duration-300 ${active ? 'flex-row' : 'flex-col'}`}>

          {/* ── Input ── */}
          <div className={`transition-all duration-300 ${active ? 'w-72 shrink-0' : 'w-full'}`}>
            <textarea
              ref={textareaRef}
              value={active ? session.text : text}
              onChange={e => setText(e.target.value)}
              disabled={active}
              rows={active ? 6 : 5}
              autoFocus
              placeholder={`例：\n"我认为宁德时代未来会跑输大盘，因为电动车渗透率增速放缓、竞争加剧且碳酸锂价格持续下行"\n\n"NVDA is bullish — AI capex from hyperscalers is accelerating and GPU supply remains constrained"`}
              className="w-full px-4 py-3 rounded-xl bg-[#161b27] border border-[#2a3245] text-[#e8eaf0] text-sm placeholder-[#3d4a63] focus:outline-none focus:border-blue-500 disabled:opacity-50 transition-all resize-none leading-relaxed"
            />
            <div className="flex justify-end mt-1">
              {(() => {
                const isChinese = /[一-龥]/.test(text)
                const count = isChinese
                  ? text.length
                  : (text.trim() === '' ? 0 : text.trim().split(/\s+/).length)
                const over = isChinese ? count > 100 : count > 30
                const label = isChinese ? `${count} / 建议 100 字以内` : `${count} words / suggest under 30`
                return (
                  <span className={`text-xs transition-colors ${over ? 'text-yellow-500' : 'text-[#3d4a63]'}`}>
                    {label}
                  </span>
                )
              })()}
            </div>
            {!active && (
              <div className="mt-3 flex flex-wrap gap-2">
                {thesisExamples.map(example => (
                  <button
                    key={example.label}
                    type="button"
                    onClick={() => useExample(example.text)}
                    className="rounded-full border border-[#2a3245] bg-[#0f1420] px-3 py-1.5 text-xs font-medium text-[#8fa3c7] transition-colors hover:border-blue-500/50 hover:bg-blue-500/10 hover:text-blue-300"
                  >
                    {example.label}示例
                  </button>
                ))}
              </div>
            )}
            <div className="flex gap-3 mt-4">
              <button
                onClick={() => navigate('/')}
                className="px-4 py-2 rounded-lg border border-[#2a3245] text-sm text-[#6b7a99] hover:text-[#e8eaf0] hover:border-[#3b4a6b] transition-all"
              >
                {active ? 'Back' : 'Cancel'}
              </button>
              <button
                onClick={handleSubmit}
                disabled={!text.trim() || active}
                className="flex-1 flex items-center justify-center gap-2 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-medium transition-all"
              >
                <Sparkles size={14} />
                解析并创建
              </button>
            </div>
          </div>

          {/* ── Thinking Panel ── */}
          {active && (
            <div className="flex-1 min-w-0 rounded-xl border border-[#2a3245] bg-[#0d1117] overflow-hidden">
              <div className="px-4 py-2.5 flex items-center gap-2" style={{ borderBottom: '1px solid #1e2435' }}>
                <span className="w-2.5 h-2.5 rounded-full bg-red-500/70" />
                <span className="w-2.5 h-2.5 rounded-full bg-yellow-500/70" />
                <span className="w-2.5 h-2.5 rounded-full bg-green-500/70" />
                <span className="text-xs text-[#3d4a63] ml-2 font-mono">thesis-agent</span>
              </div>

              <div className="p-4 font-mono text-sm space-y-1.5 min-h-[200px] max-h-[420px] overflow-y-auto">
                {shown.map((s, i) => (
                  <div key={i} className="flex items-start gap-2 animate-[fadeSlideIn_0.2s_ease-out]">
                    {s.kind === 'section' && (
                      <p className="text-[#6b7a99] text-xs mt-2 mb-0.5 uppercase tracking-wider w-full">
                        — {s.text}
                      </p>
                    )}
                    {s.kind === 'pending' && (
                      <>
                        <span className="text-[#3d4a63] mt-0.5 shrink-0">›</span>
                        <span className="text-[#6b7a99] text-xs">{s.text}</span>
                      </>
                    )}
                    {s.kind === 'ok' && (
                      <>
                        <span className="text-green-500 shrink-0 mt-0.5">✓</span>
                        <span className="text-[#c8ccd8] text-xs">{s.text}</span>
                      </>
                    )}
                    {s.kind === 'error' && (
                      <>
                        <span className="text-red-400 shrink-0 mt-0.5">✗</span>
                        <span className="text-red-400 text-xs">{s.text}</span>
                      </>
                    )}
                    {s.kind === 'done' && (
                      <div className="flex items-center gap-2 mt-2 pt-2 w-full" style={{ borderTop: '1px solid #1e2435' }}>
                        <CheckCircle size={14} className="text-green-400 shrink-0" />
                        <span className="text-green-400 text-xs font-semibold">{s.text}</span>
                      </div>
                    )}
                  </div>
                ))}
                {!done && shown.length > 0 && shown[shown.length - 1].kind !== 'done' && (
                  <span className="text-[#3d4a63] animate-pulse text-xs">▌</span>
                )}
                <div ref={bottomRef} />
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
