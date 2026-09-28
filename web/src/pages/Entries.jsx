import React, { useState, useEffect, useContext } from 'react'
import { api } from '../api'
import { t } from '../i18n'
import { ToastCtx, ConfigCtx } from '../App'
import StatusBar from '../components/StatusBar'
import Modal, { Switch, Field } from '../components/Modal'

const emptyEntry = () => ({
  enable: true, host: '', port: 19131, max_session: 100,
  heartbeat: { enable: false, manual_only: false, interval: '5s', timeout: '2s', retries: 3 },
})

export default function Entries() {
  const [entries, setEntries] = useState([])
  const [editing, setEditing] = useState(null) // {index, data}
  const toast = useContext(ToastCtx)
  const { cfg, refresh } = useContext(ConfigCtx)

  const load = () => api('/api/entry').then(d => setEntries(d.entries || [])).catch(e => toast(e.message, true))
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [])

  function save() {
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.entry = c.entry || []
    let idx = editing.index
    if (idx >= 0) {
      // 先用已存下标 (弹窗期间配置未变时直接命中), 不匹配再按 key 重定位,
      // 防止配置被外部修改导致错位覆盖
      const at = c.entry[idx]
      if (!at || keyOf(at) !== editing.key) {
        idx = findIdx(c.entry, editing.key)
        if (idx < 0) { toast(t('common.entryGone'), true); setEditing(null); return }
      }
      c.entry[idx] = editing.data
    } else c.entry.push(editing.data)
    api('/api/config', 'PUT', c)
      .then(r => { toast(t('common.saved') + (r.restart_required ? t('common.restartSuffix') : '')); setEditing(null); refresh(); load() })
      .catch(e => toast(e.message, true))
  }

  function findIdx(list, key) { return (list || []).findIndex(e => keyOf(e) === key) }

  function keyOf(e) { return e.host.includes(':') ? `[${e.host}]:${e.port}` : `${e.host}:${e.port}` }

  function openEdit(key, i) {
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    // 优先用渲染下标 (同 key 多条时区分), 仅在不匹配时退化为 key 查找
    let idx = i
    const at = cfg.entry && cfg.entry[i]
    if (!at || keyOf(at) !== key) idx = findIdx(cfg.entry, key)
    if (idx < 0) { toast(t('common.cfgChanged'), true); refresh(); return }
    setEditing({ index: idx, key, data: JSON.parse(JSON.stringify(cfg.entry[idx])) })
  }

  function del(key, i) {
    if (!cfg) { toast(t('common.cfgLoading'), true); return }
    if (!confirm(t('ent.delConfirm', { key }))) return
    // 与 openEdit 一致: 优先渲染下标 (重复 key 的禁用条目), 不匹配再退化 key 查找
    let idx = i
    const at = cfg.entry && cfg.entry[i]
    if (!at || keyOf(at) !== key) idx = findIdx(cfg.entry, key)
    if (idx < 0) { toast(t('common.cfgChanged'), true); refresh(); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.entry.splice(idx, 1)
    api('/api/config', 'PUT', c).then(() => { toast(t('common.deleted')); refresh(); load() }).catch(e => toast(e.message, true))
  }

  const d = editing && editing.data
  return (
    <>
      <div style={{ marginBottom: 12 }}>
        <button className="btn primary" onClick={() => setEditing({ index: -1, key: '', data: emptyEntry() })}>{t('ent.add')}</button>
      </div>
      {entries.map((e, i) => (
        <div className="card" key={e.key + '#' + i}>
          <h3><span className={`dot ${e.healthy ? 'up' : 'down'}`}></span>{e.key} {e.enable ? '' : t('common.disabled')}</h3>
          <div className="row">
            <span>{t('ent.sessions')} <b>{e.active_sessions}</b> / {e.max_session || t('ent.unlimited')}</span>
            {e.stats
              ? <><span>{t('common.probeStats', { total: e.stats.total_probes, failed: e.stats.failed_probes })}</span>
                  <span>RTT <b>{e.stats.last_rtt_ms}ms</b></span></>
              : <span>{t('common.noHeartbeat')}</span>}
            <span style={{ flex: 1 }}></span>
            <button className="btn small" onClick={() => openEdit(e.key, i)}>{t('common.edit')}</button>
            <button className="btn small danger" onClick={() => del(e.key, i)}>{t('common.delete')}</button>
          </div>
          <StatusBar stats={e.stats} />
        </div>
      ))}
      {editing && (
        <Modal title={editing.index >= 0 ? t('ent.editTitle') : t('ent.addTitle')} onClose={() => setEditing(null)} onSave={save}>
          <Field label={t('ent.host')}><input type="text" value={d.host} onChange={e => setEditing({ ...editing, data: { ...d, host: e.target.value } })} /></Field>
          <Field label={t('common.port')}><input type="number" value={d.port} onChange={e => setEditing({ ...editing, data: { ...d, port: +e.target.value } })} /></Field>
          <Field label={t('ent.maxSession')}><input type="number" value={d.max_session} onChange={e => setEditing({ ...editing, data: { ...d, max_session: +e.target.value } })} /></Field>
          <Switch label={t('common.enable')} value={d.enable} onChange={v => setEditing({ ...editing, data: { ...d, enable: v } })} />
          <Switch label={t('common.hbEnable')} value={d.heartbeat.enable} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, enable: v } } })} />
          <Switch label={t('common.manualOnly')} value={d.heartbeat.manual_only} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, manual_only: v } } })} />
          <Field label={t('common.hbInterval')}><input type="text" value={d.heartbeat.interval} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, interval: e.target.value } } })} /></Field>
          <Field label={t('common.hbTimeout')}><input type="text" value={d.heartbeat.timeout} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, timeout: e.target.value } } })} /></Field>
          <Field label={t('common.hbRetries')}><input type="number" value={d.heartbeat.retries} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, retries: +e.target.value } } })} /></Field>
        </Modal>
      )}
    </>
  )
}
