import React, { useContext } from 'react'
import { api } from '../api'
import { ToastCtx, ConfigCtx } from '../App'
import { Switch, Field } from '../components/Modal'

const SCHEMA = [
  { group: '日志', fields: [
    ['log.level', '级别', 'select', ['debug', 'info', 'warn', 'error']],
    ['log.format', '格式', 'select', ['text', 'json']],
    ['log.file', '日志文件 (留空仅控制台)', 'text'],
    ['log.max_size', '单文件大小 (MB, 0=100)', 'number'],
    ['log.max_backups', '保留份数 (0 不限)', 'number'],
    ['log.max_age', '保留天数 (0 不限)', 'number'],
    ['log.compress', '压缩旧日志', 'bool'],
  ]},
  { group: '网关', fields: [
    ['gateway.host', '监听主机', 'text'],
    ['gateway.port', '监听端口', 'number'],
    ['gateway.token', '访问令牌 (显示 *** 表示不修改)', 'text'],
    ['gateway.verify_identity', '验证玩家身份 JWT', 'bool'],
    ['gateway.relay_only', '中继模式 (隐藏客户端真实地址)', 'bool'],
    ['gateway.motd_cache', 'MOTD 缓存时长 (如 5s, 0s 不缓存)', 'text'],
    ['gateway.api_auth_exempt', '免认证 API 路由 (每行一个)', 'list'],
  ]},
  { group: 'TLS', fields: [
    ['gateway.tls.enable', '启用 TLS', 'bool'],
    ['gateway.tls.dual', '同端口双协议 (明文 HTTP + HTTPS)', 'bool'],
    ['gateway.tls.cert', '证书 (PEM)', 'text'],
    ['gateway.tls.key', '私钥', 'text'],
  ]},
  { group: '指标', fields: [['gateway.metrics.enable', '启用 Prometheus (/api/metrics)', 'bool']]},
  { group: '访问控制', fields: [
    ['gateway.access.mode', '名单模式', 'select', ['off', 'blacklist', 'whitelist']],
    ['gateway.access.xuids', 'XUID 名单 (每行一个)', 'list'],
    ['gateway.access.webhook.enable', '启用 webhook', 'bool'],
    ['gateway.access.webhook.url', 'webhook 地址', 'text'],
    ['gateway.access.webhook.timeout', 'webhook 超时', 'text'],
  ]},
  { group: '限流', fields: [
    ['gateway.rate_limit.enable', '启用限流', 'bool'],
    ['gateway.rate_limit.interval', '窗口', 'text'],
    ['gateway.rate_limit.max_joins', '每窗口最大 join', 'number'],
    ['gateway.rate_limit.max_keys', '最大跟踪 key 数', 'number'],
  ]},
  { group: '复用器', fields: [
    ['multiplexer.host', '监听主机', 'text'],
    ['multiplexer.port', '监听端口', 'number'],
  ]},
  { group: '会话超时', fields: [
    ['session.signaled_timeout', '等待首个 STUN', 'text'],
    ['session.active_idle_timeout', '活跃转空闲', 'text'],
    ['session.idle_reap_timeout', '空闲回收', 'text'],
    ['session.tuple_stale_timeout', '5-tuple 软状态', 'text'],
  ]},
  { group: '统计', fields: [
    ['stats.status_history_size', '状态历史上限', 'number'],
    ['stats.flush_interval', '落盘合并间隔 (如 30s)', 'text'],
  ]},
]

function getPath(o, p) { return p.split('.').reduce((a, k) => (a == null ? a : a[k]), o) }
function setPath(o, p, v) { const ks = p.split('.'); const last = ks.pop(); ks.reduce((a, k) => a[k] ??= {}, o)[last] = v }

export default function Config() {
  const toast = useContext(ToastCtx)
  const { cfg, refresh } = useContext(ConfigCtx)

  if (!cfg) return <div className="card">加载中...</div>

  function save() {
    const c = JSON.parse(JSON.stringify(cfg))
    for (const g of SCHEMA) for (const [key, , type] of g.fields) {
      const el = document.getElementById('cfg-' + key)
      let v
      if (type === 'bool') v = el.checked
      else if (type === 'number') v = +el.value
      else if (type === 'list') v = el.value.split('\n').map(s => s.trim()).filter(Boolean)
      else v = el.value
      setPath(c, key, v)
    }
    api('/api/config', 'PUT', c)
      .then(r => toast('已保存' + (r.restart_required ? ' (部分变更需重启生效)' : '')))
      .catch(e => toast(e.message, true))
  }

  function reload() {
    api('/api/config/reload', 'POST')
      .then(() => { toast('已重载'); refresh() })
      .catch(e => toast(e.message, true))
  }

  return (
    <>
      {SCHEMA.map(g => (
        <div className="card" key={g.group}>
          <h3>{g.group}</h3>
          <div className="grid">
            {g.fields.map(([key, label, type, opts]) => {
              const id = 'cfg-' + key
              const v = getPath(cfg, key)
              if (type === 'bool') return <BoolField key={key} id={id} label={label} value={!!v} />
              if (type === 'select') return (
                <Field key={key} label={label}>
                  <select id={id} defaultValue={v}>{opts.map(o => <option key={o} value={o}>{o}</option>)}</select>
                </Field>)
              if (type === 'number') return <Field key={key} label={label}><input type="number" id={id} defaultValue={v} /></Field>
              if (type === 'list') return <Field key={key} label={label}><textarea id={id} rows={3} defaultValue={(v || []).join('\n')} /></Field>
              return <Field key={key} label={label}><input type="text" id={id} defaultValue={v ?? ''} /></Field>
            })}
          </div>
        </div>
      ))}
      <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
        <button className="btn primary" onClick={save}>保存配置</button>
        <button className="btn" onClick={reload}>从文件重载</button>
      </div>
    </>
  )
}

function BoolField({ id, label, value }) {
  return (
    <label className="fld"><span>{label}</span>
      <label className="switch">
        <input type="checkbox" id={id} defaultChecked={value} /><i></i>
      </label>
    </label>
  )
}
