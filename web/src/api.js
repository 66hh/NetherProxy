let token = localStorage.getItem('np_token') || ''

export function getToken() { return token }

export function setToken(t) {
  token = t
  if (t) localStorage.setItem('np_token', t)
  else localStorage.removeItem('np_token')
}

export async function api(path, method = 'GET', body, opts = {}) {
  const hadToken = !!token
  const resp = await fetch(path, {
    method,
    headers: Object.assign(
      { Authorization: 'Bearer ' + token },
      body ? { 'Content-Type': 'application/json' } : {}),
    body: body ? JSON.stringify(body) : undefined,
  })
  if (resp.status === 401) {
    // 会话过期才刷新回登录页; 登录尝试本身由调用方提示
    if (hadToken && !opts.noRedirect) {
      setToken('')
      location.reload()
    }
    throw new Error('unauthorized')
  }
  const data = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new Error(data.details || data.error || 'HTTP ' + resp.status)
  return data
}
