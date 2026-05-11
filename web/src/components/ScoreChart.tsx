import { LineChart, Line, XAxis, YAxis, Tooltip, ReferenceLine, ResponsiveContainer } from 'recharts'
import type { ScorePoint } from '../api/client'

export function ScoreChart({ data }: { data: ScorePoint[] }) {
  return (
    <ResponsiveContainer width="100%" height={160}>
      <LineChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: -24 }}>
        <XAxis dataKey="date" tick={{ fill: '#6b7a99', fontSize: 11 }} tickLine={false} axisLine={false} />
        <YAxis domain={[0, 1]} tick={{ fill: '#6b7a99', fontSize: 11 }} tickLine={false} axisLine={false} tickFormatter={v => v.toFixed(1)} />
        <Tooltip
          contentStyle={{ background: '#161b27', border: '1px solid #2a3245', borderRadius: 8, fontSize: 12 }}
          labelStyle={{ color: '#6b7a99' }}
          formatter={v => [Number(v ?? 0).toFixed(3), 'score']}
        />
        <ReferenceLine y={0.5} stroke="#2a3245" strokeDasharray="4 3" />
        <Line type="monotone" dataKey="score" stroke="#3b82f6" strokeWidth={2} dot={false} activeDot={{ r: 4, fill: '#3b82f6' }} />
      </LineChart>
    </ResponsiveContainer>
  )
}
