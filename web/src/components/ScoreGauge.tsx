interface Props { score: number; size?: number }

export function ScoreGauge({ score, size = 120 }: Props) {
  const r = (size / 2) * 0.75
  const cx = size / 2
  const cy = size / 2
  const startAngle = -210
  const endAngle = 30
  const totalDeg = endAngle - startAngle
  const fillDeg = totalDeg * score

  const toRad = (d: number) => (d * Math.PI) / 180
  const pt = (angle: number, radius: number) => ({
    x: cx + radius * Math.cos(toRad(angle)),
    y: cy + radius * Math.sin(toRad(angle)),
  })

  const arcPath = (fromDeg: number, toDeg: number, rad: number) => {
    const s = pt(fromDeg, rad)
    const e = pt(toDeg, rad)
    const large = toDeg - fromDeg > 180 ? 1 : 0
    return `M ${s.x} ${s.y} A ${rad} ${rad} 0 ${large} 1 ${e.x} ${e.y}`
  }

  const scoreColor = score >= 0.65 ? '#22c55e' : score >= 0.45 ? '#f59e0b' : '#ef4444'
  const trackColor = '#1e2435'
  const strokeW = size * 0.09

  return (
    <div className="relative flex items-center justify-center" style={{ width: size, height: size }}>
      <svg width={size} height={size}>
        <path d={arcPath(startAngle, endAngle, r)} fill="none" stroke={trackColor} strokeWidth={strokeW} strokeLinecap="round" />
        {score > 0 && (
          <path d={arcPath(startAngle, startAngle + fillDeg, r)} fill="none" stroke={scoreColor} strokeWidth={strokeW} strokeLinecap="round" />
        )}
      </svg>
      <div className="absolute flex flex-col items-center">
        <span className="font-bold" style={{ fontSize: size * 0.22, color: scoreColor }}>
          {(score * 100).toFixed(0)}
        </span>
        <span style={{ fontSize: size * 0.10, color: '#6b7a99' }}>score</span>
      </div>
    </div>
  )
}
