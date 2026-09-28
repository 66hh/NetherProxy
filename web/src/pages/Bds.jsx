import React, { useState, useEffect, useContext } from 'react'
import { api } from '../api'
import { t } from '../i18n'
import { ToastCtx, ConfigCtx } from '../App'
import StatusBar from '../components/StatusBar'
import Modal, { Switch, Field } from '../components/Modal'

const emptyBds = () => ({
  enable: true, domain: '', host: '', port: 19132,
  heartbeat: { enable: false, manual_only: false, interval: '5s', timeout: '2s', retries: 3 },
})

export default function Bds() {
  const [list, setList] = useState([])
  const [editing, setEditing] = useState(null)
  const toast = useContext(ToastCtx)
  const { cfg, refresh } = useContext(ConfigCtx)

  const load = () => api('/api/bds').then(d => setList(d.bds || [])).catch(e => toast(e.message, true))
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [])

  function save() {
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.bds = c.bds || []
    let idx = editing.index
    if (idx >= 0) {
      // 先用已存下标 (弹窗期间配置未变时直接命中), 不匹配再按 key 重定位,
      // 防止配置被外部修改导致错位覆盖
      const at = c.bds[idx]
      if (!at || keyOf(at) !== editing.key) {
        idx = findIdx(c.bds, editing.key)
        if (idx < 0) { toast(t('common.entryGone'), true); setEditing(null); return }
      }
      c.bds[idx] = editing.data
    } else c.bds.push(editing.data)
    api('/api/config', 'PUT', c)
      .then(r => { toast(t('common.saved') + (r.restart_required ? t('common.restartSuffix') : '')); setEditing(null); refresh(); load() })
      .catch(e => toast(e.message, true))
  }

  function findIdx(list, key) { return (list || []).findIndex(b => keyOf(b) === key) }

  function keyOf(e) { return e.host.includes(':') ? `[${e.host}]:${e.port}` : `${e.host}:${e.port}` }

  function openEdit(key, i) {
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    // 优先用渲染下标 (同 key 多条时区分), 仅在不匹配时退化为 key 查找
    let idx = i
    const at = cfg.bds && cfg.bds[i]
    if (!at || keyOf(at) !== key) idx = findIdx(cfg.bds, key)
    if (idx < 0) { toast(t('common.cfgChanged'), true); refresh(); return }
    setEditing({ index: idx, key, data: JSON.parse(JSON.stringify(cfg.bds[idx])) })
  }

  function del(key, i) {
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    if (!confirm(t('bds.delConfirm', { key }))) return
    // 与 openEdit 一致: 优先渲染下标 (重复 key 的禁用条目), 不匹配再退化 key 查找
    let idx = i
    const at = cfg.bds && cfg.bds[i]
    if (!at || keyOf(at) !== key) idx = findIdx(cfg.bds, key)
    if (idx < 0) { toast(t('common.cfgChanged'), true); refresh(); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.bds.splice(idx, 1)
    api('/api/config', 'PUT', c).then(() => { toast(t('common.deleted')); refresh(); load() }).catch(e => toast(e.message, true))
  }

  const d = editing && editing.data
  return (
    <>
      <div style={{ marginBottom: 12 }}>
        <button className="btn primary" onClick={() => setEditing({ index: -1, key: '', data: emptyBds() })}>{t('bds.add')}</button>
      </div>
      {list.map((b, i) => (
        <div className="card" key={b.key + b.domain + '#' + i}>
          <h3><span className={`dot ${b.healthy ? 'up' : 'down'}`}></span>{b.domain} → {b.key} {b.enable ? '' : t('common.disabled')}</h3>
          <div className="row">
            {b.stats
              ? <><span>{t('common.probeStats', { total: b.stats.total_probes, failed: b.stats.failed_probes })}</span>
                  <span>RTT <b>{b.stats.last_rtt_ms}ms</b></span></>
              : <span>{t('common.noHeartbeat')}</span>}
            <span style={{ flex: 1 }}></span>
            <button className="btn small" onClick={() => openEdit(b.key, i)}>{t('common.edit')}</button>
            <button className="btn small danger" onClick={() => del(b.key, i)}>{t('common.delete')}</button>
          </div>
          <StatusBar stats={b.stats} />
        </div>
      ))}
      {editing && (
        <Modal title={editing.index >= 0 ? t('bds.editTitle') : t('bds.addTitle')} onClose={() => setEditing(null)} onSave={save}>
          <Field label={t('bds.domain')}><input type="text" value={d.domain} onChange={e => setEditing({ ...editing, data: { ...d, domain: e.target.value } })} /></Field>
          <Field label={t('common.host')}><input type="text" value={d.host} onChange={e => setEditing({ ...editing, data: { ...d, host: e.target.value } })} /></Field>
          <Field label={t('common.port')}><input type="number" value={d.port} onChange={e => setEditing({ ...editing, data: { ...d, port: +e.target.value } })} /></Field>
          <Switch label={t('common.enable')} value={d.enable} onChange={v => setEditing({ ...editing, data: { ...d, enable: v } })} />
          <Switch label={t('common.hbEnable')} value={d.heartbeat.enable} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, enable: v } } })} />
          <Switch label={t('common.manualOnly')} value={d.heartbeat.manual_only} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, manual_only: v } } })} />
          <Field label={t('common.probeInterval')}><input type="text" value={d.heartbeat.interval} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, interval: e.target.value } } })} /></Field>
          <Field label={t('common.hbTimeout')}><input type="text" value={d.heartbeat.timeout} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, timeout: e.target.value } } })} /></Field>
          <Field label={t('common.hbRetries')}><input type="number" value={d.heartbeat.retries} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, retries: +e.target.value } } })} /></Field>
        </Modal>
      )}
    </>
  )
}
