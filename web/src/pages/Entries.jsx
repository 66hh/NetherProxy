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
    const c = JSON.parse(JSON.stringify(cfg))
    if (editing.index >= 0) c.entry[editing.index] = editing.data
    else c.entry.push(editing.data)
    api('/api/config', 'PUT', c)
      .then(r => { toast('已保存' + (r.restart_required ? ' (部分变更需重启生效)' : '')); setEditing(null); refresh(); load() })
      .catch(e => toast(e.message, true))
  }

  function del(i) {
    if (!confirm('删除线路 ' + entries[i].key + '?')) return
    const c = JSON.parse(JSON.stringify(cfg))
    c.entry.splice(i, 1)
    api('/api/config', 'PUT', c).then(() => { toast('已删除'); refresh(); load() }).catch(e => toast(e.message, true))
  }

  const d = editing && editing.data
  return (
    <>
      <div style={{ marginBottom: 12 }}>
        <button className="btn primary" onClick={() => setEditing({ index: -1, data: emptyEntry() })}>新增线路</button>
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
            <button className="btn small" onClick={() => setEditing({ index: i, data: JSON.parse(JSON.stringify(cfg.entry[i])) })}>编辑</button>
            <button className="btn small danger" onClick={() => del(i)}>删除</button>
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
