import React, { useState, useEffect, useContext } from 'react'
import { api } from '../api'
import { ToastCtx, ConfigCtx } from '../App'

function fmtBytes(n) {
  if (n > 1e9) return (n / 1e9).toFixed(2) + ' GB'
  if (n > 1e6) return (n / 1e6).toFixed(2) + ' MB'
  if (n > 1e3) return (n / 1e3).toFixed(1) + ' KB'
  return n + ' B'
}
function fmtAge(s) {
  if (s > 3600) return (s / 3600).toFixed(1) + 'h'
  if (s > 60) return (s / 60).toFixed(0) + 'm'
  return s + 's'
}

export default function Sessions() {
  const [sessions, setSessions] = useState([])
  const toast = useContext(ToastCtx)
  const { cfg, refresh } = useContext(ConfigCtx)

  const load = () => api('/api/session').then(d => setSessions(d.sessions || [])).catch(() => {})
  useEffect(() => {
    load()
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  }, [])

  function kill(ufrag) {
    if (!confirm('确认掐断会话 ' + ufrag + '?')) return
    api('/api/session/' + encodeURIComponent(ufrag), 'DELETE')
      .then(() => { toast('已掐断'); load() })
      .catch(e => toast(e.message, true))
  }

  function block(xuid, name) {
    if (!xuid) { toast('该会话无 XUID', true); return }
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    const mode = cfg.gateway.access.mode
    const msg = mode === 'whitelist'
      ? '当前为白名单模式, 拉黑将切换为黑名单模式并仅保留该玩家, 确认?'
      : `将玩家 ${name} (${xuid}) 加入黑名单?`
    if (!confirm(msg)) return
    const c = JSON.parse(JSON.stringify(cfg))
    c.gateway.access.mode = 'blacklist'
    // whitelist 的原名单语义相反, 切换时不可沿用
    c.gateway.access.xuids = mode === 'whitelist'
      ? [xuid]
      : [...new Set([...(c.gateway.access.xuids || []), xuid])]
    api('/api/config', 'PUT', c)
      .then(() => { toast('已拉黑'); refresh() })
      .catch(e => toast(e.message, true))
  }

  return (
    <div className="card">
      <table>
        <thead><tr>
          <th>玩家</th><th>XUID</th><th>客户端</th><th>线路</th><th>状态</th><th>下行/上行</th><th>时长</th><th>操作</th>
        </tr></thead>
        <tbody>
          {sessions.map(s => (
            <tr key={s.ufrag}>
              <td><b>{s.player || '-'}</b></td>
              <td className="mono">{s.xuid || '-'}</td>
              <td className="mono">{s.client}</td>
              <td className="mono">{s.entry}</td>
              <td>{s.state}</td>
              <td className="mono">{fmtBytes(s.tx_bytes)} / {fmtBytes(s.rx_bytes)}</td>
              <td>{fmtAge(s.age_seconds)}</td>
              <td>
                <button className="btn small danger" onClick={() => kill(s.ufrag)}>掐断</button>{' '}
                <button className="btn small" onClick={() => block(s.xuid, s.player)}>拉黑</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {!sessions.length && <div className="row" style={{ marginTop: 8 }}>暂无会话</div>}
    </div>
  )
}
