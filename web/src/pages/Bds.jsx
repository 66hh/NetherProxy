import React, { useState, useEffect, useContext } from 'react'
import { api } from '../api'
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
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.bds = c.bds || []
    let idx = editing.index
    if (idx >= 0) {
      // 保存时按 key 重新定位, 防止弹窗期间配置被外部修改导致错位覆盖
      idx = findIdx(c.bds, editing.key)
      if (idx < 0) { toast('该条目已被删除, 保存取消', true); setEditing(null); return }
      c.bds[idx] = editing.data
    } else c.bds.push(editing.data)
    api('/api/config', 'PUT', c)
      .then(r => { toast('已保存' + (r.restart_required ? ' (部分变更需重启生效)' : '')); setEditing(null); refresh(); load() })
      .catch(e => toast(e.message, true))
  }

  function findIdx(list, key) { return (list || []).findIndex(b => b.host + ':' + b.port === key) }

  function keyOf(e) { return e.host.includes(':') ? `[${e.host}]:${e.port}` : `${e.host}:${e.port}` }

  function openEdit(key, i) {
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    // 优先用渲染下标 (同 key 多条时区分), 仅在不匹配时退化为 key 查找
    let idx = i
    const at = cfg.bds && cfg.bds[i]
    if (!at || keyOf(at) !== key) idx = findIdx(cfg.bds, key)
    if (idx < 0) { toast('配置已变化, 请重试', true); refresh(); return }
    setEditing({ index: idx, key, data: JSON.parse(JSON.stringify(cfg.bds[idx])) })
  }

  function del(key) {
    if (!cfg) { toast('配置加载中, 请稍后', true); return }
    if (!confirm('删除 BDS ' + key + '?')) return
    const idx = findIdx(cfg.bds, key)
    if (idx < 0) { toast('配置已变化, 请重试', true); refresh(); return }
    const c = JSON.parse(JSON.stringify(cfg))
    c.bds.splice(idx, 1)
    api('/api/config', 'PUT', c).then(() => { toast('已删除'); refresh(); load() }).catch(e => toast(e.message, true))
  }

  const d = editing && editing.data
  return (
    <>
      <div style={{ marginBottom: 12 }}>
        <button className="btn primary" onClick={() => setEditing({ index: -1, key: '', data: emptyBds() })}>新增 BDS</button>
      </div>
      {list.map((b, i) => (
        <div className="card" key={b.key + b.domain}>
          <h3><span className={`dot ${b.healthy ? 'up' : 'down'}`}></span>{b.domain} → {b.key} {b.enable ? '' : '(已禁用)'}</h3>
          <div className="row">
            {b.stats
              ? <><span>探测 <b>{b.stats.total_probes}</b> 次, 失败 <b>{b.stats.failed_probes}</b></span>
                  <span>RTT <b>{b.stats.last_rtt_ms}ms</b></span></>
              : <span>心跳未开启</span>}
            <span style={{ flex: 1 }}></span>
            <button className="btn small" onClick={() => openEdit(b.key, i)}>编辑</button>
            <button className="btn small danger" onClick={() => del(b.key)}>删除</button>
          </div>
          <StatusBar stats={b.stats} />
        </div>
      ))}
      {editing && (
        <Modal title={(editing.index >= 0 ? '编辑' : '新增') + ' BDS'} onClose={() => setEditing(null)} onSave={save}>
          <Field label="绑定域名 (支持 * 通配)"><input type="text" value={d.domain} onChange={e => setEditing({ ...editing, data: { ...d, domain: e.target.value } })} /></Field>
          <Field label="主机"><input type="text" value={d.host} onChange={e => setEditing({ ...editing, data: { ...d, host: e.target.value } })} /></Field>
          <Field label="端口"><input type="number" value={d.port} onChange={e => setEditing({ ...editing, data: { ...d, port: +e.target.value } })} /></Field>
          <Switch label="启用" value={d.enable} onChange={v => setEditing({ ...editing, data: { ...d, enable: v } })} />
          <Switch label="健康探测" value={d.heartbeat.enable} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, enable: v } } })} />
          <Switch label="仅记录统计 (手动下线)" value={d.heartbeat.manual_only} onChange={v => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, manual_only: v } } })} />
          <Field label="探测间隔"><input type="text" value={d.heartbeat.interval} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, interval: e.target.value } } })} /></Field>
          <Field label="超时"><input type="text" value={d.heartbeat.timeout} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, timeout: e.target.value } } })} /></Field>
          <Field label="失败次数阈值"><input type="number" value={d.heartbeat.retries} onChange={e => setEditing({ ...editing, data: { ...d, heartbeat: { ...d.heartbeat, retries: +e.target.value } } })} /></Field>
        </Modal>
      )}
    </>
  )
}
