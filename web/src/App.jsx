import React, { useState, useEffect, useCallback } from 'react'
import { api, getToken, setToken } from './api'
import Sessions from './pages/Sessions'
import Entries from './pages/Entries'
import Bds from './pages/Bds'
import Config from './pages/Config'

export const ToastCtx = React.createContext(() => {})
export const ConfigCtx = React.createContext({ cfg: null, refresh: () => {} })

export default function App() {
  const [authed, setAuthed] = useState(false)
  const [tab, setTab] = useState('sessions')
  const [cfg, setCfg] = useState(null)
  const [toastMsg, setToastMsg] = useState(null)

  const toast = useCallback((msg, isErr) => {
    setToastMsg({ msg, isErr })
    setTimeout(() => setToastMsg(null), 3000)
  }, [])

  const refresh = useCallback(() =>
    api('/api/config').then(c => setCfg(c)).catch(e => toast(e.message, true)), [toast])

  useEffect(() => {
    if (!getToken()) return
    api('/api/session')
      .then(() => { setAuthed(true); refresh() })
      .catch(() => {})
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
            api('/api/session')
              .then(() => { setAuthed(true); refresh() })
              .catch(() => { setToken(''); toast('令牌无效', true) })
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

  const tabs = [['sessions', '会话'], ['entries', '线路'], ['bds', 'BDS'], ['config', '配置']]
  return (
    <ToastCtx.Provider value={toast}>
      <ConfigCtx.Provider value={{ cfg, refresh }}>
        <header>
          <h1>NetherProxy</h1>
          <span className="spacer"></span>
          <span className="row"><span className="dot up"></span>已连接</span>
          <button className="btn" onClick={() => { setToken(''); location.reload() }}>退出</button>
        </header>
        <nav>
          {tabs.map(([k, label]) => (
            <button key={k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{label}</button>
          ))}
        </nav>
        <main>
          {tab === 'sessions' && <Sessions />}
          {tab === 'entries' && <Entries />}
          {tab === 'bds' && <Bds />}
          {tab === 'config' && <Config />}
        </main>
        {toastMsg && <div className={`toast${toastMsg.isErr ? ' err' : ''}`}>{toastMsg.msg}</div>}
      </ConfigCtx.Provider>
    </ToastCtx.Provider>
  )
}
