import React, { useState, useEffect, useCallback, useRef } from 'react'
import { api, getToken, setToken } from './api'
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
  return <span className="row"><span className={`dot ${ok ? 'up' : 'down'}`}></span>{ok ? '已连接' : '连接断开'}</span>
}

export default function App() {
  const [authed, setAuthed] = useState(false)
  const [tab, setTab] = useState('dashboard')
  const [cfg, setCfg] = useState(null)
  const [cfgVersion, setCfgVersion] = useState(0)
  const [toastMsg, setToastMsg] = useState(null)

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
      .catch(e => { if (e.network) toast('无法连接到服务, 请确认代理已启动', true) })
  }, [])

  if (!authed) {
    return (
      <ToastCtx.Provider value={toast}>
        <div className="login"><div className="card">
          <h3>输入访问令牌</h3>
          <form onSubmit={e => {
            e.preventDefault()
            const t = e.target.token.value.trim()
            setToken(t)
            api('/api/session', 'GET', undefined, { noRedirect: true })
              .then(() => { setAuthed(true); refresh() })
              .catch(err => {
                setToken('')
                // 区分: 网络错误=服务不可达, 401=令牌无效, 其他原样展示
                if (err.network) toast('无法连接到服务, 请确认代理已启动', true)
                else if (err.message === 'unauthorized') toast('令牌无效', true)
                else toast(err.message, true)
              })
          }}>
            <label className="fld"><span>Token (gateway.token)</span>
              <input type="password" name="token" autoFocus /></label>
            <button className="btn primary" style={{ width: '100%' }}>登录</button>
          </form>
        </div></div>
        {toastMsg && <div className={`toast${toastMsg.isErr ? ' err' : ''}`}>{toastMsg.msg}</div>}
      </ToastCtx.Provider>
    )
  }

  const tabs = [['dashboard', '概览'], ['sessions', '会话'], ['entries', '线路'], ['bds', 'BDS'], ['logs', '日志'], ['config', '配置']]
  return (
    <ToastCtx.Provider value={toast}>
      <ConfigCtx.Provider value={{ cfg, version: cfgVersion, refresh }}>
        <header>
          <h1>NetherProxy</h1>
          <span className="spacer"></span>
          <ConnState />
          <button className="btn" onClick={() => { setToken(''); location.reload() }}>退出</button>
        </header>
        <nav>
          {tabs.map(([k, label]) => (
            <button key={k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{label}</button>
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
