import React, { useState, useEffect, useContext } from 'react'
import { api } from '../api'
import { t } from '../i18n'
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
    if (!confirm(t('sess.killConfirm', { ufrag }))) return
    api('/api/session/' + encodeURIComponent(ufrag), 'DELETE')
      .then(() => { toast(t('sess.killed')); load() })
      .catch(e => toast(e.message, true))
  }

  function block(xuid, name) {
    if (!xuid) { toast(t('sess.noXuid'), true); return }
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    const mode = cfg.gateway.access.mode
    const msg = mode === 'whitelist'
      ? t('sess.blockConfirmWl')
      : t('sess.blockConfirm', { name, xuid })
    if (!confirm(msg)) return
    const c = JSON.parse(JSON.stringify(cfg))
    c.gateway.access.mode = 'blacklist'
    // whitelist 的原名单语义相反, 切换时不可沿用
    c.gateway.access.xuids = mode === 'whitelist'
      ? [xuid]
      : [...new Set([...(c.gateway.access.xuids || []), xuid])]
    api('/api/config', 'PUT', c)
      .then(() => { toast(t('sess.blocked')); refresh() })
      .catch(e => toast(e.message, true))
  }

  return (
    <div className="card">
      <table>
        <thead><tr>
          <th>{t('sess.player')}</th><th>XUID</th><th>{t('sess.client')}</th><th>{t('sess.entry')}</th>
          <th>{t('sess.state')}</th><th>{t('sess.traffic')}</th><th>{t('sess.age')}</th><th>{t('sess.ops')}</th>
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
                <button className="btn small danger" onClick={() => kill(s.ufrag)}>{t('sess.kill')}</button>{' '}
                <button className="btn small" onClick={() => block(s.xuid, s.player)}>{t('sess.block')}</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {!sessions.length && <div className="row" style={{ marginTop: 8 }}>{t('sess.empty')}</div>}
    </div>
  )
}
