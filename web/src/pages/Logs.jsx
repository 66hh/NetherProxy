import React, { useState, useEffect, useRef } from 'react'
import { api } from '../api'

const LEVELS = ['debug', 'info', 'warn', 'error']
const LV_COLOR = { DEBUG: 'var(--gray)', INFO: 'var(--accent)', WARN: 'var(--yellow)', ERROR: 'var(--red)' }

export default function Logs() {
  const [logs, setLogs] = useState([])
  const [level, setLevel] = useState('info')
  const [follow, setFollow] = useState(true)
  const boxRef = useRef(null)

  useEffect(() => {
    const load = () => api(`/api/log?tail=300&level=${level}`).then(d => setLogs(d.logs || [])).catch(() => {})
    load()
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  }, [level])

  useEffect(() => {
    if (follow && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight
  }, [logs, follow])

  return (
    <div className="card">
      <div className="row" style={{ marginBottom: 10 }}>
        <span>级别</span>
        <select value={level} onChange={e => setLevel(e.target.value)} style={{ width: 120 }}>
          {LEVELS.map(l => <option key={l} value={l}>{l}</option>)}
        </select>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} style={{ width: 'auto' }} />
          跟随滚动
        </label>
        <span style={{ flex: 1 }}></span>
        <span>{logs.length} 条</span>
      </div>
      <div className="log-box" ref={boxRef}>
        {logs.map((l, i) => (
          <div key={i} className="log-line">
            <span className="log-time">{new Date(l.time).toLocaleTimeString('zh-CN', { hour12: false })}</span>
            <span className="log-level" style={{ color: LV_COLOR[l.Level] || 'var(--dim)' }}>{l.Level}</span>
            <span className="log-msg">{l.Msg}{l.Attrs ? <span className="log-attrs"> {l.Attrs}</span> : null}</span>
          </div>
        ))}
        {!logs.length && <div className="row">暂无日志</div>}
      </div>
    </div>
  )
}
