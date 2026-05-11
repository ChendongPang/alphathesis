import { useState } from 'react'
import { BrowserRouter, Routes, Route, NavLink, Navigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider, useQueryClient } from '@tanstack/react-query'
import { LayoutDashboard, Plus, Activity, LogOut } from 'lucide-react'
import { Dashboard } from './pages/Dashboard'
import { ThesisDetail } from './pages/ThesisDetail'
import { ReportDetail } from './pages/ReportDetail'
import { SubmitThesis } from './pages/SubmitThesis'
import { LoginPage } from './pages/LoginPage'
import { authStorage, api } from './api/client'
import { analysisSession } from './state/analysisSession'

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 30_000 } },
})

function AppShell() {
  const [authed, setAuthed] = useState(() => !!authStorage.getToken())
  const qc = useQueryClient()

  const handleLogin = () => setAuthed(true)

  const handleLogout = async () => {
    try { await api.logout() } catch { /* ignore */ }
    authStorage.clear()
    qc.clear()
    setAuthed(false)
  }

  if (!authed) {
    return <LoginPage onLogin={handleLogin} />
  }

  return (
    <BrowserRouter>
      <div className="flex h-screen overflow-hidden" style={{ background: '#0a0e17' }}>
        <aside className="w-[220px] shrink-0 flex flex-col py-5" style={{ borderRight: '1px solid #1e2435' }}>
          <div className="px-5 mb-7">
            <div className="flex items-center gap-2">
              <Activity size={20} className="text-blue-500" />
              <span className="font-bold text-[#e8eaf0] text-lg tracking-tight">AlphaThesis</span>
            </div>
            <p className="text-xs mt-0.5" style={{ color: '#3d4a63' }}>AI Investment Monitor</p>
          </div>

          <nav className="flex-1 px-3 space-y-1">
            <NavLink
              to="/"
              end
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors ${isActive ? 'bg-blue-600/20 text-blue-400' : 'text-[#6b7a99] hover:text-[#9ca3af] hover:bg-[#161b27]'}`
              }
            >
              <LayoutDashboard size={16} />
              Dashboard
            </NavLink>
            <NavLink
              to="/submit"
              onClick={() => analysisSession.clear()}
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors ${isActive ? 'bg-blue-600/20 text-blue-400' : 'text-[#6b7a99] hover:text-[#9ca3af] hover:bg-[#161b27]'}`
              }
            >
              <Plus size={16} />
              New Thesis
            </NavLink>
          </nav>

          <div className="px-4 pt-4 space-y-2" style={{ borderTop: '1px solid #1e2435' }}>
            <button
              onClick={handleLogout}
              className="flex items-center gap-2 w-full px-3 py-2 rounded-lg text-xs text-[#6b7a99] hover:text-red-400 hover:bg-red-500/10 transition-colors"
            >
              <LogOut size={13} />
              退出登录
            </button>
          </div>
        </aside>

        <main className="flex-1 overflow-hidden" style={{ background: '#0a0e17' }}>
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/thesis/:id" element={<ThesisDetail />} />
            <Route path="/thesis/:id/report/:reportId" element={<ReportDetail />} />
            <Route path="/submit" element={<SubmitThesis />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AppShell />
    </QueryClientProvider>
  )
}
