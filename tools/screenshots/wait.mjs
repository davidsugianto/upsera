// Polls the monitors API of the throwaway stack until the demo monitors
// have been checked (no fixed sleeps), then asserts the state the screenshots
// rely on. Usage: node wait.mjs initial | node wait.mjs seeded <epoch-ms>
import process from 'node:process'

const [phase, since = '0'] = process.argv.slice(2)
const { UPSERA_URL: base, UPSERA_TOKEN: token, DEMO_TEAM_ID: team } = process.env
const deadline = Date.now() + 120_000

async function monitors() {
  const res = await fetch(`${base}/api/teams/${team}/monitors`, { headers: { authorization: `Bearer ${token}` } })
  if (!res.ok) throw new Error(`list monitors: HTTP ${res.status}`)
  const body = await res.json()
  return Array.isArray(body) ? body : (body.monitors ?? [])
}

function checked(m) {
  if (m.type === 'push') return true
  const at = m.state?.last_check_at
  return !!at && new Date(at).getTime() > Number(since)
}

for (;;) {
  const list = await monitors()
  if (list.length >= 6 && list.every(checked)) {
    if (phase === 'seeded') {
      const by = Object.fromEntries(list.map((m) => [m.name, m.state?.status]))
      const want = { 'checkout-api': 'degraded', website: 'up', docs: 'up', database: 'up', dns: 'up', 'nightly-backup': 'up' }
      const bad = Object.entries(want).filter(([n, s]) => by[n] !== s)
      if (bad.length > 0) {
        console.error('unexpected monitor states (are the example.com hosts reachable from Docker?):')
        for (const [n, s] of bad) console.error(`  ${n}: want ${s}, got ${by[n]}`)
        process.exit(1)
      }
    }
    break
  }
  if (Date.now() > deadline) {
    console.error(`timed out waiting for the demo monitors (${phase})`)
    process.exit(1)
  }
  await new Promise((r) => setTimeout(r, 1000))
}
