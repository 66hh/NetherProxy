import React, { useContext } from 'react'
import { api } from '../api'
import { t } from '../i18n'
import { ToastCtx, ConfigCtx } from '../App'
import { Switch, Field } from '../components/Modal'

// 字段标签与分组名存 i18n key, 渲染时翻译
const SCHEMA = [
  { group: 'cfg.group.log', fields: [
    ['log.level', 'cfg.f.log.level', 'select', ['debug', 'info', 'warn', 'error']],
    ['log.format', 'cfg.f.log.format', 'select', ['text', 'json']],
    ['log.file', 'cfg.f.log.file', 'text'],
    ['log.max_size', 'cfg.f.log.max_size', 'number'],
    ['log.max_backups', 'cfg.f.log.max_backups', 'number'],
    ['log.max_age', 'cfg.f.log.max_age', 'number'],
    ['log.compress', 'cfg.f.log.compress', 'bool'],
    ['log.buffer_size', 'cfg.f.log.buffer_size', 'number'],
  ]},
  { group: 'cfg.group.gateway', fields: [
    ['gateway.host', 'cfg.f.gateway.host', 'text'],
    ['gateway.port', 'cfg.f.gateway.port', 'number'],
    ['gateway.token', 'cfg.f.gateway.token', 'text'],
    ['gateway.verify_identity', 'cfg.f.gateway.verify_identity', 'bool'],
    ['gateway.relay_only', 'cfg.f.gateway.relay_only', 'bool'],
    ['gateway.motd_cache', 'cfg.f.gateway.motd_cache', 'text'],
    ['gateway.api_auth_exempt', 'cfg.f.gateway.api_auth_exempt', 'list'],
  ]},
  { group: 'cfg.group.tls', fields: [
    ['gateway.tls.enable', 'cfg.f.gateway.tls.enable', 'bool'],
    ['gateway.tls.dual', 'cfg.f.gateway.tls.dual', 'bool'],
    ['gateway.tls.cert', 'cfg.f.gateway.tls.cert', 'text'],
    ['gateway.tls.key', 'cfg.f.gateway.tls.key', 'text'],
  ]},
  { group: 'cfg.group.metrics', fields: [['gateway.metrics.enable', 'cfg.f.gateway.metrics.enable', 'bool']]},
  { group: 'cfg.group.access', fields: [
    ['gateway.access.mode', 'cfg.f.gateway.access.mode', 'select', ['off', 'blacklist', 'whitelist']],
    ['gateway.access.xuids', 'cfg.f.gateway.access.xuids', 'list'],
    ['gateway.access.webhook.enable', 'cfg.f.gateway.access.webhook.enable', 'bool'],
    ['gateway.access.webhook.url', 'cfg.f.gateway.access.webhook.url', 'text'],
    ['gateway.access.webhook.timeout', 'cfg.f.gateway.access.webhook.timeout', 'text'],
  ]},
  { group: 'cfg.group.rateLimit', fields: [
    ['gateway.rate_limit.enable', 'cfg.f.gateway.rate_limit.enable', 'bool'],
    ['gateway.rate_limit.interval', 'cfg.f.gateway.rate_limit.interval', 'text'],
    ['gateway.rate_limit.max_joins', 'cfg.f.gateway.rate_limit.max_joins', 'number'],
    ['gateway.rate_limit.max_keys', 'cfg.f.gateway.rate_limit.max_keys', 'number'],
  ]},
  { group: 'cfg.group.mux', fields: [
    ['multiplexer.host', 'cfg.f.multiplexer.host', 'text'],
    ['multiplexer.port', 'cfg.f.multiplexer.port', 'number'],
  ]},
  { group: 'cfg.group.session', fields: [
    ['session.signaled_timeout', 'cfg.f.session.signaled_timeout', 'text'],
    ['session.active_idle_timeout', 'cfg.f.session.active_idle_timeout', 'text'],
    ['session.idle_reap_timeout', 'cfg.f.session.idle_reap_timeout', 'text'],
    ['session.tuple_stale_timeout', 'cfg.f.session.tuple_stale_timeout', 'text'],
  ]},
  { group: 'cfg.group.stats', fields: [
    ['stats.status_history_size', 'cfg.f.stats.status_history_size', 'number'],
    ['stats.flush_interval', 'cfg.f.stats.flush_interval', 'text'],
  ]},
  { group: 'cfg.group.notify', fields: [
    ['notify.enable', 'cfg.f.notify.enable', 'bool'],
    ['notify.url', 'cfg.f.notify.url', 'text'],
    ['notify.timeout', 'cfg.f.notify.timeout', 'text'],
    ['notify.on_recovery', 'cfg.f.notify.on_recovery', 'bool'],
  ]},
]

function getPath(o, p) { return p.split('.').reduce((a, k) => (a == null ? a : a[k]), o) }
function setPath(o, p, v) { const ks = p.split('.'); const last = ks.pop(); ks.reduce((a, k) => a[k] ??= {}, o)[last] = v }

export default function Config() {
  const toast = useContext(ToastCtx)
  const { cfg, version, refresh } = useContext(ConfigCtx)

  if (!cfg) return <div className="card">{t('common.loading')}</div>

  function save() {
    const c = JSON.parse(JSON.stringify(cfg))
    for (const g of SCHEMA) for (const [key, , type] of g.fields) {
      const el = document.getElementById('cfg-' + key)
      let v
      if (type === 'bool') v = el.checked
      else if (type === 'number') v = el.value === '' ? getPath(cfg, key) : +el.value // 清空保留原值
      else if (type === 'list') v = el.value.split('\n').map(s => s.trim()).filter(Boolean)
      else v = el.value
      setPath(c, key, v)
    }
    api('/api/config', 'PUT', c)
      .then(r => { toast(t('common.saved') + (r.restart_required ? t('common.restartSuffix') : '')); refresh() })
      .catch(e => toast(e.message, true))
  }

  function reload() {
    api('/api/config/reload', 'POST')
      .then(() => { toast(t('cfg.reloaded')); refresh() })
      .catch(e => toast(e.message, true))
  }

  return (
    <div key={version}>
      {SCHEMA.map(g => (
        <div className="card" key={g.group}>
          <h3>{t(g.group)}</h3>
          <div className="grid">
            {g.fields.map(([key, labelKey, type, opts]) => {
              const id = 'cfg-' + key
              const label = t(labelKey)
              const v = getPath(cfg, key)
              if (type === 'bool') return <BoolField key={key} id={id} label={label} value={!!v} />
              if (type === 'select') return (
                <Field key={key} label={label}>
                  <select id={id} defaultValue={v}>
                    {!opts.includes(v) && <option value={v}>{String(v)} {t('cfg.invalid')}</option>}
                    {opts.map(o => <option key={o} value={o}>{o}</option>)}
                  </select>
                </Field>)
              if (type === 'number') return <Field key={key} label={label}><input type="number" id={id} defaultValue={v} /></Field>
              if (type === 'list') return <Field key={key} label={label}><textarea id={id} rows={3} defaultValue={(v || []).join('\n')} /></Field>
              return <Field key={key} label={label}><input type="text" id={id} defaultValue={v ?? ''} /></Field>
            })}
          </div>
        </div>
      ))}
      <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
        <button className="btn primary" onClick={save}>{t('cfg.save')}</button>
        <button className="btn" onClick={reload}>{t('cfg.reload')}</button>
      </div>
    </div>
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
