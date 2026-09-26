let token = localStorage.getItem('np_token') || ''

export function getToken() { return token }

export function setToken(t) {
  token = t
  if (t) localStorage.setItem('np_token', t)
  else localStorage.removeItem('np_token')
}

export async function api(path, method = 'GET', body) {
  const resp = await fetch(path, {
    method,
    headers: Object.assign(
      { Authorization: 'Bearer ' + token },
      body ? { 'Content-Type': 'application/json' } : {}),
    body: body ? JSON.stringify(body) : undefined,
  })
  if (resp.status === 401) {
    setToken('')
    location.reload()
    throw new Error('unauthorized')
  }
  const data = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new Error(data.details || data.error || 'HTTP ' + resp.status)
  return data
}
