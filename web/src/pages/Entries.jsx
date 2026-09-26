import React, { useState, useEffect, useContext } from 'react'
import { api } from '../api'
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
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.entry = c.entry || []
    let idx = editing.index
    if (idx >= 0) {
      // 保存时按 key 重新定位, 防止弹窗期间配置被外部修改导致错位覆盖
      idx = findIdx(c.entry, editing.key)
      if (idx < 0) { toast('该条目已被删除, 保存取消', true); setEditing(null); return }
      c.entry[idx] = editing.data
    } else c.entry.push(editing.data)
    api('/api/config', 'PUT', c)
      .then(r => { toast('已保存' + (r.restart_required ? ' (部分变更需重启生效)' : '')); setEditing(null); refresh(); load() })
      .catch(e => toast(e.message, true))
  }

  function findIdx(list, key) {
    return (list || []).findIndex(e => (e.host.includes(':') ? `[${e.host}]:${e.port}` : `${e.host}:${e.port}`) === key)
  }

  function openEdit(key) {
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    const idx = findIdx(cfg.entry, key)
    if (idx < 0) { toast('配置已变化, 请重试', true); refresh(); return }
    setEditing({ index: idx, key, data: JSON.parse(JSON.stringify(cfg.entry[idx])) })
  }

  function del(key) {
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    if (!confirm('删除线路 ' + key + '?')) return
    const idx = findIdx(cfg.entry, key)
    if (idx < 0) { toast('配置已变化, 请重试', true); refresh(); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.entry.splice(idx, 1)
    api('/api/config', 'PUT', c).then(() => { toast('已删除'); refresh(); load() }).catch(e => toast(e.message, true))
  }

  const d = editing && editing.data
  return (
    <>
      <div style={{ marginBottom: 12 }}>
        <button className="btn primary" onClick={() => setEditing({ index: -1, key: '', data: emptyEntry() })}>新增线路</button>
      </div>
      {entries.map((e, i) => (
        <div className="card" key={e.key}>
          <h3><span className={`dot ${e.healthy ? 'up' : 'down'}`}></span>{e.key} {e.enable ? '' : '(已禁用)'}</h3>
          <div className="row">
            <span>活跃会话 <b>{e.active_sessions}</b> / {e.max_session || '不限'}</span>
            {e.stats
              ? <><span>探测 <b>{e.stats.total_probes}</b> 次, 失败 <b>{e.stats.failed_probes}</b></span>
                  <span>RTT <b>{e.stats.last_rtt_ms}ms</b></span></>
              : <span>心跳未开启</span>}
            <span style={{ flex: 1 }}></span>
            <button className="btn small" onClick={() => openEdit(e.key)}>编辑</button>
            <button className="btn small danger" onClick={() => del(e.key)}>删除</button>
          </div>
          <StatusBar stats={e.stats} />
        </div>
      ))}
      {editing && (
        <Modal title={(editing.index >= 0 ? '编辑' : '新增') + '线路'} onClose={() => setEditing(null)} onSave={save}>
          <Field label="主机 (公网地址)"><input type="text" value={d.host} onChange={e => setEditing({ ...editing, data: { ...d, host: e.target.value } })} /></Field>
          <Field label="端口"><input type="number" value={d.port} onChange={e => setEditing({ ...editing, data: { ...d, port: +e.target.value } })} /></Field>
          <Field label="最大会话数 (0 不限)"><input type="number" value={d.max_session} onChange={e => setEditing({ ...editing, data: { ...d, max_session: +e.target.value } })} /></Field>
          <Switch label="启用" value={d.enable} onChange={v => setEditing({ ...editing, data: { ...d, enable: v } })} />
          <Switch label="心跳探测" value={d.heartbeat.enable} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, enable: v } } })} />
          <Switch label="仅记录统计 (手动下线)" value={d.heartbeat.manual_only} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, manual_only: v } } })} />
          <Field label="心跳间隔"><input type="text" value={d.heartbeat.interval} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, interval: e.target.value } } })} /></Field>
          <Field label="超时"><input type="text" value={d.heartbeat.timeout} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, timeout: e.target.value } } })} /></Field>
          <Field label="失败次数阈值"><input type="number" value={d.heartbeat.retries} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, retries: +e.target.value } } })} /></Field>
        </Modal>
      )}
    </>
  )
}
