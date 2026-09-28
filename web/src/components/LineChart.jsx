import React, { useState } from 'react'
import { parseTime } from '../api'
import { t, locale } from '../i18n'

// LineChart 轻量 SVG 折线图 (无依赖): 渐变区域填充、X 轴时间刻度、
// 悬停十字线 + 数值提示。series: [{key, color, label}]
export default function LineChart({ points, series, height = 160, unit = '' }) {
  const [hover, setHover] = useState(null)
  if (!points || points.length < 2) {
    return <div className="chart-empty">{t('chart.empty')}</div>
  }
  const W = 800, H = height, padL = 44, padR = 8, padT = 10, padB = 18
  const iw = W - padL - padR, ih = H - padT - padB

  let max = 0
  for (const p of points) for (const s of series) max = Math.max(max, p[s.key] || 0)
  if (max <= 0) max = 1
  max *= 1.15

  const n = points.length
  const x = i => padL + (i / (n - 1)) * iw
  const y = v => padT + ih - (v / max) * ih

  const ticks = [0, 0.25, 0.5, 0.75, 1].map(f => ({ v: max * f, y: y(max * f) }))
  // X 轴时间刻度: 首/中/尾
  const xTicks = [...new Set([0, Math.floor((n - 1) / 2), n - 1])].map(i => ({
    x: x(i), label: fmtTime(points[i].time), anchor: i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle',
  }))

  // viewBox 非等比缩放, 鼠标坐标按比例换算回去
  const onMove = e => {
    const rect = e.currentTarget.getBoundingClientRect()
    const vx = ((e.clientX - rect.left) / rect.width) * W
    const i = Math.max(0, Math.min(n - 1, Math.round(((vx - padL) / iw) * (n - 1))))
    setHover(i)
  }
  const hp = hover != null ? points[hover] : null

  return (
    <div className="chart-wrap">
      <svg viewBox={`0 0 ${W} ${H}`} className="line-chart" preserveAspectRatio="none"
        onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
        <defs>
          {series.map(s => (
            <linearGradient key={s.key} id={`grad-${s.key}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={s.color} stopOpacity="0.3" />
              <stop offset="100%" stopColor={s.color} stopOpacity="0.02" />
            </linearGradient>
          ))}
        </defs>
        {ticks.map((t, i) => (
          <g key={i}>
            <line x1={padL} y1={t.y} x2={W - padR} y2={t.y} className="grid-line" />
            <text x={padL - 4} y={t.y + 3} className="tick" textAnchor="end">{fmtVal(t.v)}{unit}</text>
          </g>
        ))}
        {xTicks.map((t, i) => (
          <text key={i} x={t.x} y={H - 4} className="tick" textAnchor={t.anchor}>{t.label}</text>
        ))}
        {series.map(s => {
          const line = points.map((p, i) => `${x(i)},${y(p[s.key] || 0)}`).join(' ')
          const area = `${padL},${padT + ih} ` + line + ` ${x(n - 1)},${padT + ih}`
          return (
            <g key={s.key}>
              <polygon points={area} fill={`url(#grad-${s.key})`} stroke="none" />
              <polyline fill="none" stroke={s.color} strokeWidth="1.8" points={line}
                vectorEffect="non-scaling-stroke" />
            </g>
          )
        })}
        {hover != null && (
          <g>
            <line x1={x(hover)} y1={padT} x2={x(hover)} y2={padT + ih} className="crosshair" />
            {series.map(s => (
              <circle key={s.key} cx={x(hover)} cy={y(hp[s.key] || 0)} r="3" fill={s.color} />
            ))}
          </g>
        )}
      </svg>
      {hp && (
        <div className="chart-tip">
          <span className="tip-time">{fmtTimeFull(hp.time)}</span>
          {series.map(s => (
            <span key={s.key}><i style={{ background: s.color }}></i>
              {s.label || s.key} {fmtVal(hp[s.key] || 0)}{unit}</span>
          ))}
        </div>
      )}
    </div>
  )
}

function fmtTime(s) {
  const d = parseTime(s)
  if (isNaN(d)) return ''
  return d.toLocaleTimeString(locale(), { hour12: false })
}

function fmtTimeFull(s) {
  const d = parseTime(s)
  if (isNaN(d)) return ''
  return d.toLocaleString(locale(), { hour12: false })
}

function fmtVal(v) {
  if (v >= 1e9) return (v / 1e9).toFixed(1) + 'G'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'K'
  return v.toFixed(0)
}
