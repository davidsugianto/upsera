// Creates the first admin (POST /api/setup) of the throwaway stack and a
// team API token, then prints shell `export` lines for run.sh to eval. The
// password and the token only ever live in that process environment.
import { randomBytes } from 'node:crypto'

const base = process.env.UPSERA_URL
if (!base) throw new Error('UPSERA_URL is required')

const email = 'demo@example.com'
const password = randomBytes(12).toString('hex') // 24 chars

async function call(path, init = {}, csrf, cookie) {
  const headers = { 'content-type': 'application/json', ...(init.headers ?? {}) }
  if (csrf) headers['x-csrf-token'] = csrf
  if (cookie) headers.cookie = cookie
  const res = await fetch(base + path, { ...init, headers })
  if (!res.ok) throw new Error(`${init.method ?? 'GET'} ${path}: HTTP ${res.status}`)
  return res
}

const status = await (await call('/api/setup')).json()
if (!status.needed) throw new Error('the stack already has users; expected a fresh database')

const res = await call('/api/setup', {
  method: 'POST',
  body: JSON.stringify({ email, name: 'Demo User', password, team_name: 'Example Cloud' }),
})
const me = await res.json()
const cookie = res.headers.getSetCookie().map((c) => c.split(';')[0]).join('; ')
const teamId = me.teams[0].id

const tok = await (
  await call(
    `/api/teams/${teamId}/tokens`,
    { method: 'POST', body: JSON.stringify({ name: 'screenshots', scope: 'write' }) },
    me.csrf_token,
    cookie,
  )
).json()

const q = (s) => `'${String(s).replaceAll("'", `'\\''`)}'`
console.log(`export DEMO_EMAIL=${q(email)}`)
console.log(`export DEMO_PASSWORD=${q(password)}`)
console.log(`export DEMO_TEAM_ID=${q(teamId)}`)
console.log(`export UPSERA_TOKEN=${q(tok.token)}`)
