// Drives a real browser through the Upsera pages of the demo stack and writes
// the documentation screenshots. Fails (exit 1) on browser console errors,
// uncaught page errors and failed or 4xx/5xx API requests, so it doubles as a
// smoke test of the dashboard.
// Usage: node capture.mjs <output-dir>   (env: UPSERA_URL, DEMO_EMAIL,
// DEMO_PASSWORD, DEMO_TEAM_ID, UPSERA_TOKEN)
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { chromium } from 'playwright'

const outDir = resolve(process.argv[2] ?? '../../site/public/screenshots')
const { UPSERA_URL: base, DEMO_EMAIL: email, DEMO_PASSWORD: password, DEMO_TEAM_ID: team, UPSERA_TOKEN: token } = process.env
for (const [k, v] of Object.entries({ UPSERA_URL: base, DEMO_EMAIL: email, DEMO_PASSWORD: password, DEMO_TEAM_ID: team, UPSERA_TOKEN: token })) {
  if (!v) throw new Error(`${k} is required`)
}
mkdirSync(outDir, { recursive: true })

const problems = []
const noSLO = /\/api\/teams\/\d+\/monitors\/\d+\/slo$/ // 404 = the monitor has no SLO

function watch(page) {
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const url = msg.location().url ?? ''
    // The browser logs every failed request as a console error; failures are
    // judged (and the expected 404 for "no SLO" tolerated) by the response
    // listener below, so only report the others here.
    if (/Failed to load resource/.test(msg.text()) && url) return
    problems.push(`console error on ${page.url()}: ${msg.text()}`)
  })
  page.on('pageerror', (err) => problems.push(`page error on ${page.url()}: ${err.message}`))
  page.on('requestfailed', (req) => {
    const why = req.failure()?.errorText ?? 'failed'
    if (why === 'net::ERR_ABORTED') return // navigation or query cancellation
    problems.push(`request failed: ${req.method()} ${req.url()}: ${why}`)
  })
  page.on('response', (res) => {
    if (res.status() < 400) return
    if (res.status() === 404 && noSLO.test(new URL(res.url()).pathname)) return
    problems.push(`HTTP ${res.status()}: ${res.request().method()} ${res.url()} (on ${page.url()})`)
  })
}

async function api(path) {
  const res = await fetch(`${base}${path}`, { headers: { authorization: `Bearer ${token}` } })
  if (!res.ok) throw new Error(`GET ${path}: HTTP ${res.status}`)
  return res.json()
}

const list = await api(`/api/teams/${team}/monitors`)
const monitors = Array.isArray(list) ? list : (list.monitors ?? [])
const idOf = (name) => {
  const m = monitors.find((x) => x.name === name)
  if (!m) throw new Error(`demo monitor ${name} not found`)
  return m.id
}

const browser = await chromium.launch()
const viewport = { width: 1440, height: 900 }
const settle = (page) => page.waitForTimeout(300) // let CSS transitions and chart animations finish

async function newContext(theme, storageState) {
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 1, colorScheme: theme, storageState, locale: 'en-US', timezoneId: 'UTC' })
  await ctx.addInitScript((t) => localStorage.setItem('upsera.theme', t), theme)
  return ctx
}

async function shot(page, name, opts = {}) {
  await settle(page)
  await page.screenshot({ path: resolve(outDir, name), ...opts })
  console.log(`  ${name}`)
}

// Log in once through the form and reuse the session for every context.
const loginCtx = await newContext('light')
const login = await loginCtx.newPage()
await login.goto(`${base}/login`)
await login.getByLabel('Email').fill(email)
await login.getByLabel('Password').fill(password)
await login.getByRole('button', { name: 'Log in' }).click()
await login.waitForURL(new RegExp(`/t/${team}`))
const session = await loginCtx.storageState()
await loginCtx.close()

async function openMonitors(page) {
  await page.goto(`${base}/t/${team}`, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: /Monitors/ }).waitFor()
  await page.getByText('checkout-api').first().waitFor()
  await page.getByText('1 Degraded').waitFor() // the DEGRADED count in the page header
  await page.getByText('nightly-backup').first().waitFor()
}

// Dark first (the app default), then light for every other page.
const dark = await newContext('dark', session)
{
  const page = await dark.newPage()
  watch(page)
  await openMonitors(page)
  await shot(page, 'dashboard-dark.png')
}
await dark.close()

const light = await newContext('light', session)
const page = await light.newPage()
watch(page)

console.log('capturing')
await openMonitors(page)
await shot(page, 'dashboard-light.png')

// The list filtered by a tag: the degraded monitor stays visible.
await page.getByRole('button', { name: 'production' }).click()
await page.getByText('website').first().waitFor()
await page.getByText('docs').first().waitFor({ state: 'detached' })
await shot(page, 'monitors-list.png')

const checkout = idOf('checkout-api')
await page.goto(`${base}/t/${team}/monitors/${checkout}`, { waitUntil: 'networkidle' })
const budget = page.getByRole('heading', { name: 'Error budget' })
await budget.waitFor()
await page.getByText('Recent heartbeats').waitFor()
await page.getByText('remaining').first().waitFor()
await budget.scrollIntoViewIfNeeded() // the status header above it stays in view when both fit
await shot(page, 'monitor-detail.png')

await page.goto(`${base}/t/${team}/monitors/${idOf('website')}/edit`, { waitUntil: 'networkidle' })
const sloBox = page.getByLabel('Track an SLO for this monitor')
await sloBox.waitFor()
if (!(await sloBox.isChecked())) await sloBox.check()
await page.getByText('Advanced').click() // expand the burn thresholds
await page.getByLabel('Fast burn threshold').waitFor()
await page.getByText('Service level objective').first().evaluate((el) => el.scrollIntoView({ block: 'start' }))
await page.evaluate(() => window.scrollBy(0, -40))
// Chromium renders number inputs in the OS locale (99,9 on a machine set to
// Indonesia), which would make the picture depend on who ran it: show them as
// plain text with their dot-decimal value.
await page.evaluate(() => document.querySelectorAll('input[type=number]').forEach((i) => (i.type = 'text')))
await shot(page, 'slo-form.png')

await page.goto(`${base}/t/${team}/reports`, { waitUntil: 'networkidle' })
await page.getByText('Missed').first().waitFor()
await page.getByText('Met', { exact: true }).first().waitFor()
await shot(page, 'reports.png')

await page.goto(`${base}/status/demo`, { waitUntil: 'networkidle' })
await page.getByText('Example Cloud status').first().waitFor()
await page.getByText('Checkout API').first().waitFor()
await shot(page, 'status-page.png')

await page.goto(`${base}/admin`, { waitUntil: 'networkidle' })
await page.getByRole('heading', { name: 'Probes' }).waitFor()
await page.getByText('frankfurt').first().waitFor()
await page.getByRole('heading', { name: 'Probes' }).evaluate((el) => el.scrollIntoView({ block: 'start' }))
await page.evaluate(() => window.scrollBy(0, -40))
await shot(page, 'probes.png')

await page.goto(`${base}/t/${team}/channels`, { waitUntil: 'networkidle' })
await page.getByText('ops-webhook').first().waitFor()
await page.getByText('team-slack').first().waitFor()
await shot(page, 'alert-channels.png')

await browser.close()

if (problems.length > 0) {
  console.error(`\n${problems.length} browser problem(s):`)
  for (const p of [...new Set(problems)]) console.error(`  - ${p}`)
  process.exit(1)
}
console.log('done: no console errors or failed requests')
