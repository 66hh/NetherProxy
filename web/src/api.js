let token = localStorage.getItem('np_token') || ''

export function getToken() { return token }

export function setToken(t) {
  token = t
  if (t) localStorage.setItem('np_token', t)
  else localStorage.removeItem('np_token')
}

export async function api(path, method = 'GET', body, opts = {}) {
  const hadToken = !!token
  let resp
  try {
    resp = await fetch(path, {
      method,
      headers: Object.assign(
        { Authorization: 'Bearer ' + token },
        body ? { 'Content-Type': 'application/json' } : {}),
      body: body ? JSON.stringify(body) : undefined,
    })
  } catch (e) {
    // 网络层失败 (服务宕机/断网): 打上标记, 调用方可区分"服务不可达"
    e.network = true
    throw e
  }
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

// parseTime 解析后端时间戳 (Go RFC3339Nano)。JS Date 只支持毫秒精度,
// 纳秒部分截断到 3 位; 无小数的整秒格式原样解析
export function parseTime(s) {
  if (typeof s !== 'string') return new Date(NaN)
  const m = s.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(.*)$/)
  if (!m) return new Date(s)
  return new Date(m[1] + (m[2] ? '.' + m[2].slice(0, 3).padEnd(3, '0') : '') + m[3])
}
