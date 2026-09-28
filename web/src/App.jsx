import React, { useState, useEffect, useCallback, useRef } from 'react'
import { api, getToken, setToken } from './api'
import { t, getLang, setLang, LANGS } from './i18n'
import Dashboard from './pages/Dashboard'
import Sessions from './pages/Sessions'
import Entries from './pages/Entries'
import Bds from './pages/Bds'
import Config from './pages/Config'
import Logs from './pages/Logs'

export const ToastCtx = React.createContext(() => {})
export const ConfigCtx = React.createContext({ cfg: null, version: 0, refresh: () => {} })

function ConnState() {
  const [ok, setOk] = useState(true)
  useEffect(() => {
    let dead = false
    // 主动探测 (healthz 默认豁免认证): 能收到任何 HTTP 响应即视为连接正常
    const check = () => fetch('/api/healthz')
      .then(() => { if (!dead) setOk(true) })
      .catch(() => { if (!dead) setOk(false) })
    check()
    const t = setInterval(check, 5000)
    return () => { dead = true; clearInterval(t) }
  }, [])
  return <span className="row"><span className={`dot ${ok ? 'up' : 'down'}`}></span>{ok ? t('conn.ok') : t('conn.fail')}</span>
}

// LangSelect 语言切换器 (登录页与顶栏共用)
function LangSelect({ lang, onChange }) {
  return (
    <select className="lang-select" value={lang} onChange={e => onChange(e.target.value)}>
      {LANGS.map(l => <option key={l.id} value={l.id}>{l.label}</option>)}
    </select>
  )
}

export default function App() {
  const [authed, setAuthed] = useState(false)
  const [tab, setTab] = useState('dashboard')
  const [cfg, setCfg] = useState(null)
  const [cfgVersion, setCfgVersion] = useState(0)
  const [toastMsg, setToastMsg] = useState(null)
  const [lang, setLangState] = useState(getLang())

  const toastTimer = useRef(null)
  const toast = useCallback((msg, isErr) => {
    clearTimeout(toastTimer.current)
    setToastMsg({ msg, isErr })
    toastTimer.current = setTimeout(() => setToastMsg(null), 3000)
  }, [])

  const refresh = useCallback(() =>
    api('/api/config').then(c => { setCfg(c); setCfgVersion(v => v + 1) }).catch(e => toast(e.message, true)), [toast])

  useEffect(() => {
    if (!getToken()) return
    api('/api/session')
      .then(() => { setAuthed(true); refresh() })
      .catch(e => { if (e.network) toast(t('common.unreachable'), true) })
  }, [])

  // 切换语言: 更新全局状态并重渲染整树, t() 即输出新语言
  const changeLang = l => { setLang(l); setLangState(l) }

  if (!authed) {
    return (
      <ToastCtx.Provider value={toast}>
        <div className="login"><div className="card">
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
            <LangSelect lang={lang} onChange={changeLang} />
          </div>
          <h3>{t('login.title')}</h3>
          <form onSubmit={e => {
            e.preventDefault()
            const tk = e.target.token.value.trim()
            setToken(tk)
            api('/api/session', 'GET', undefined, { noRedirect: true })
              .then(() => { setAuthed(true); refresh() })
              .catch(err => {
                setToken('')
                // 区分: 网络错误=服务不可达, 401=令牌无效, 其他原样展示
                if (err.network) toast(t('common.unreachable'), true)
                else if (err.message === 'unauthorized') toast(t('login.badToken'), true)
                else toast(err.message, true)
              })
          }}>
            <label className="fld"><span>{t('login.token')}</span>
              <input type="password" name="token" autoFocus /></label>
            <button className="btn primary" style={{ width: '100%' }}>{t('login.submit')}</button>
          </form>
        </div></div>
        {toastMsg && <div className={`toast${toastMsg.isErr ? ' err' : ''}`}>{toastMsg.msg}</div>}
      </ToastCtx.Provider>
    )
  }

  const tabs = [
    ['dashboard', 'nav.dashboard'], ['sessions', 'nav.sessions'], ['entries', 'nav.entries'],
    ['bds', 'nav.bds'], ['logs', 'nav.logs'], ['config', 'nav.config'],
  ]
  return (
    <ToastCtx.Provider value={toast}>
      <ConfigCtx.Provider value={{ cfg, version: cfgVersion, refresh }}>
        <header>
          <h1>NetherProxy</h1>
          <span className="spacer"></span>
          <ConnState />
          <LangSelect lang={lang} onChange={changeLang} />
          <button className="btn" onClick={() => { setToken(''); location.reload() }}>{t('app.logout')}</button>
        </header>
        <nav>
          {tabs.map(([k, label]) => (
            <button key={k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{t(label)}</button>
          ))}
        </nav>
        <main>
          {tab === 'dashboard' && <Dashboard />}
          {tab === 'sessions' && <Sessions />}
          {tab === 'entries' && <Entries />}
          {tab === 'bds' && <Bds />}
          {tab === 'logs' && <Logs />}
          {tab === 'config' && <Config />}
        </main>
        {toastMsg && <div className={`toast${toastMsg.isErr ? ' err' : ''}`}>{toastMsg.msg}</div>}
      </ConfigCtx.Provider>
    </ToastCtx.Provider>
  )
}
