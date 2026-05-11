import { useState } from 'react'
import { Activity } from 'lucide-react'
import { api, authStorage } from '../api/client'

const SPLINE_BACKGROUND_URL = 'https://my.spline.design/robotfollowcursorforlandingpage-bvwcATVAW71tD1AhvXdaqBVB/'

interface Props {
  onLogin: () => void
}

export function LoginPage({ onLogin }: Props) {
  const [tab, setTab] = useState<'login' | 'register'>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const res = tab === 'login'
        ? await api.login(email, password)
        : await api.register(email, password, name)
      authStorage.setToken(res.token)
      onLogin()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="relative min-h-screen overflow-hidden bg-[#05070c]">
      <iframe
        src={SPLINE_BACKGROUND_URL}
        title="AlphaThesis interactive background"
        className="absolute inset-0 h-full w-full border-0"
        allow="fullscreen"
      />

      <div className="pointer-events-none relative z-10 flex min-h-screen items-center justify-end px-6 py-10 sm:px-10 lg:px-20">
        <div className="pointer-events-auto w-full max-w-sm">
          {/* Logo */}
          <div className="mb-8 flex items-center justify-center gap-2">
            <Activity size={24} className="text-blue-500" />
            <span className="font-bold text-[#e8eaf0] text-xl tracking-tight">AlphaThesis</span>
          </div>

          <div className="rounded-2xl border border-white/10 bg-[#0f1117]/88 p-8 shadow-2xl shadow-black/40">
            {/* Tabs */}
            <div className="flex gap-1 border-b border-[#1e2435] mb-6">
              {(['login', 'register'] as const).map(t => (
                <button
                  key={t}
                  onClick={() => { setTab(t); setError('') }}
                  className={`px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
                    tab === t ? 'border-blue-500 text-blue-400' : 'border-transparent text-[#6b7a99] hover:text-[#9ca3af]'
                  }`}
                >
                  {t === 'login' ? '登录' : '注册'}
                </button>
              ))}
            </div>

            <form onSubmit={submit} className="space-y-4">
              {tab === 'register' && (
                <div>
                  <label className="block text-xs text-[#6b7a99] mb-1.5">姓名</label>
                  <input
                    type="text"
                    value={name}
                    onChange={e => setName(e.target.value)}
                    placeholder="Your name"
                    className="w-full px-3 py-2 rounded-lg bg-[#161b27] border border-[#2a3245] text-[#e8eaf0] text-sm placeholder-[#3d4a63] focus:outline-none focus:border-blue-500/60 transition-colors"
                  />
                </div>
              )}

              <div>
                <label className="block text-xs text-[#6b7a99] mb-1.5">邮箱</label>
                <input
                  type="email"
                  value={email}
                  onChange={e => setEmail(e.target.value)}
                  placeholder="you@example.com"
                  required
                  className="w-full px-3 py-2 rounded-lg bg-[#161b27] border border-[#2a3245] text-[#e8eaf0] text-sm placeholder-[#3d4a63] focus:outline-none focus:border-blue-500/60 transition-colors"
                />
              </div>

              <div>
                <label className="block text-xs text-[#6b7a99] mb-1.5">密码</label>
                <input
                  type="password"
                  value={password}
                  onChange={e => setPassword(e.target.value)}
                  placeholder="••••••••"
                  required
                  className="w-full px-3 py-2 rounded-lg bg-[#161b27] border border-[#2a3245] text-[#e8eaf0] text-sm placeholder-[#3d4a63] focus:outline-none focus:border-blue-500/60 transition-colors"
                />
              </div>

              {error && (
                <p className="text-xs text-red-400 bg-red-500/10 border border-red-500/20 rounded-lg px-3 py-2">
                  {error}
                </p>
              )}

              <button
                type="submit"
                disabled={loading}
                className="w-full py-2 rounded-lg bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white text-sm font-medium transition-colors"
              >
                {loading ? '请稍候...' : tab === 'login' ? '登录' : '注册'}
              </button>
            </form>
          </div>
        </div>
      </div>
    </div>
  )
}
