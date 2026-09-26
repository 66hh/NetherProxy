import React from 'react'

// LineChart 轻量 SVG 折线图 (无依赖)
export default function LineChart({ points, series, height = 160, unit = '' }) {
  if (!points || points.length < 2) {
    return <div className="chart-empty">暂无数据</div>
  }
  const W = 800, H = height, padL = 44, padR = 8, padT = 8, padB = 18
  const iw = W - padL - padR, ih = H - padT - padB

  let max = 0
  for (const p of points) for (const s of series) max = Math.max(max, p[s.key] || 0)
  if (max <= 0) max = 1
  max *= 1.15

  const x = i => padL + (i / (points.length - 1)) * iw
  const y = v => padT + ih - (v / max) * ih

  const ticks = [0, 0.25, 0.5, 0.75, 1].map(f => ({ v: max * f, y: y(max * f) }))

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="line-chart" preserveAspectRatio="none">
      {ticks.map((t, i) => (
        <g key={i}>
          <line x1={padL} y1={t.y} x2={W - padR} y2={t.y} className="grid-line" />
          <text x={padL - 4} y={t.y + 3} className="tick" textAnchor="end">{fmtVal(t.v)}{unit}</text>
        </g>
      ))}
      {series.map(s => (
        <polyline key={s.key} fill="none" stroke={s.color} strokeWidth="1.8"
          points={points.map((p, i) => `${x(i)},${y(p[s.key] || 0)}`).join(' ')} />
      ))}
    </svg>
  )
}

function fmtVal(v) {
  if (v >= 1e9) return (v / 1e9).toFixed(1) + 'G'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'K'
  return v.toFixed(0)
}
