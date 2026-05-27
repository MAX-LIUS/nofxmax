interface RadarChartProps {
  factors: { name: string; score: number; label: string }[]
  size?: number
}

export function FactorRadarChart({ factors, size = 200 }: RadarChartProps) {
  if (!factors || factors.length < 3) return null

  const cx = size / 2
  const cy = size / 2
  const radius = size * 0.38
  const n = factors.length
  const angleStep = (2 * Math.PI) / n

  // Generate polygon points for the data
  const dataPoints = factors.map((f, i) => {
    const angle = i * angleStep - Math.PI / 2
    const r = (f.score / 100) * radius
    return { x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) }
  })

  // Generate grid rings
  const rings = [25, 50, 75, 100]

  const axes = factors.map((f, i) => {
    const angle = i * angleStep - Math.PI / 2
    const endX = cx + radius * Math.cos(angle)
    const endY = cy + radius * Math.sin(angle)
    const labelX = cx + (radius + 18) * Math.cos(angle)
    const labelY = cy + (radius + 18) * Math.sin(angle)
    return { endX, endY, labelX, labelY, label: f.label, score: f.score }
  })

  const scoreColor = (score: number) => {
    if (score >= 65) return '#0ECB81'
    if (score >= 50) return '#F0B90B'
    return '#F6465D'
  }

  return (
    <svg width={size} height={size} className="mx-auto">
      {/* Grid rings */}
      {rings.map((ring) => {
        const r = (ring / 100) * radius
        const points = Array.from({ length: n }, (_, i) => {
          const angle = i * angleStep - Math.PI / 2
          return `${cx + r * Math.cos(angle)},${cy + r * Math.sin(angle)}`
        }).join(' ')
        return (
          <polygon
            key={ring}
            points={points}
            fill="none"
            stroke="rgba(255,255,255,0.08)"
            strokeWidth="0.5"
          />
        )
      })}

      {/* Axis lines */}
      {axes.map((axis, i) => (
        <line
          key={i}
          x1={cx}
          y1={cy}
          x2={axis.endX}
          y2={axis.endY}
          stroke="rgba(255,255,255,0.1)"
          strokeWidth="0.5"
        />
      ))}

      {/* Data polygon */}
      <polygon
        points={dataPoints.map((p) => `${p.x},${p.y}`).join(' ')}
        fill="rgba(99,102,241,0.15)"
        stroke="#6366F1"
        strokeWidth="1.5"
      />

      {/* Data points */}
      {dataPoints.map((p, i) => (
        <circle
          key={i}
          cx={p.x}
          cy={p.y}
          r={3}
          fill={scoreColor(factors[i].score)}
          stroke="rgba(0,0,0,0.5)"
          strokeWidth="1"
        />
      ))}

      {/* Labels */}
      {axes.map((axis, i) => (
        <text
          key={i}
          x={axis.labelX}
          y={axis.labelY}
          textAnchor="middle"
          dominantBaseline="middle"
          fill="#848E9C"
          fontSize="8"
          fontFamily="monospace"
        >
          {axis.label.length > 4 ? axis.label.slice(0, 4) : axis.label}
        </text>
      ))}

      {/* Center score */}
      <text
        x={cx}
        y={cy}
        textAnchor="middle"
        dominantBaseline="middle"
        fill="#EAECEF"
        fontSize="12"
        fontWeight="bold"
        fontFamily="monospace"
      >
        {Math.round(factors.reduce((s, f) => s + f.score, 0) / factors.length)}
      </text>
    </svg>
  )
}
