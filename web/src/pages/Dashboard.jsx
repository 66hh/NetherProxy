import React, { useState, useEffect } from 'react'
import { api } from '../api'
import LineChart from '../components/LineChart'

function fmtRate(v) {
  if (v > 1e6) return (v / 1e6).toFixed(2) + ' MB/s'
  if (v > 1e3) return (v / 1e3).toFixed(1) + ' KB/s'
  return v.toFixed(0) + ' B/s'
}
function fmtBytes(n) {
  if (n > 1e9) return (n / 1e9).toFixed(2) + ' GB'
  if (n > 1e6) return (n / 1e6).toFixed(2) + ' MB'
  if (n > 1e3) return (n / 1e3).toFixed(1) + ' KB'
  return n + ' B'
}

export default function Dashboard() {
  const [points, setPoints] = useState([])

  useEffect(() => {
    const load = () => api('/api/stats').then(d => setPoints(d.points || [])).catch(() => {})
    load()
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  }, [])

  const cur = points.length ? points[points.length - 1] : null

  return (
    <>
      <div className="stat-cards">
        <StatCard label="活跃会话" value={cur ? cur.sessions : '-'} />
        <StatCard label="上行 (客户端→BDS)" value={cur ? fmtRate(cur.rx_rate) : '-'} color="var(--green)" />
        <StatCard label="下行 (BDS→客户端)" value={cur ? fmtRate(cur.tx_rate) : '-'} color="var(--accent)" />
        <StatCard label="健康线路" value={cur ? `${cur.entries_up}/${cur.entries_total}` : '-'} />
        <StatCard label="健康 BDS" value={cur ? `${cur.bds_up}/${cur.bds_total}` : '-'} />
        <StatCard label="累计流量" value={cur ? fmtBytes(cur.total_rx + cur.total_tx) : '-'} />
      </div>
      <div className="card">
        <h3>流量速率</h3>
        <div className="legend">
          <span><i style={{ background: 'var(--green)' }}></i>上行</span>
          <span><i style={{ background: 'var(--accent)' }}></i>下行</span>
        </div>
        <LineChart points={points} height={180} unit="/s" series={[
          { key: 'rx_rate', color: '#3fb950' },
          { key: 'tx_rate', color: '#58a6ff' },
        ]} />
      </div>
      <div className="card">
        <h3>活跃会话</h3>
        <LineChart points={points} height={120} series={[
          { key: 'sessions', color: '#d29922' },
        ]} />
      </div>
    </>
  )
}

function StatCard({ label, value, color }) {
  return (
    <div className="card stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value" style={color ? { color } : {}}>{value}</div>
    </div>
  )
}
