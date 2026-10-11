import type { Probe, RegionState } from '../api/types'
import { compareRegions } from './beats'

/** probeRunCommand is the ready-to-copy `docker run` that starts a probe with its one-time registration token. */
export function probeRunCommand(serverUrl: string, token: string): string {
  const lines = ['docker run -d --name upsera-probe --restart unless-stopped', `  -e SERVER_URL=${serverUrl}`]
  if (serverUrl.startsWith('http://')) lines.push('  -e ALLOW_INSECURE_HTTP=1')
  lines.push(`  -e PROBE_TOKEN=${token}`, '  -v upsera-probe:/data', '  upsera/upsera-probe')
  return lines.join(' \\\n')
}

/** formatAge renders an elapsed time in ms as "just now", "42s ago", "5m ago", "3h ago" or "2d ago". */
export function formatAge(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 5) return 'just now'
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

/** probeStatusLine says whether a probe has registered, or when its registration token runs out. */
export function probeStatusLine(p: Probe, now: number): string {
  if (p.registered) return 'registered'
  if (!p.registration_expires_at) return 'awaiting registration'
  const when = new Date(p.registration_expires_at).toLocaleString()
  return Date.parse(p.registration_expires_at) <= now
    ? `registration token expired ${when}`
    : `awaiting registration, expires ${when}`
}

/** regionRows lists a monitor's per-region states, "local" first. */
export function regionRows(states: Record<string, RegionState> | null | undefined): { region: string; state: RegionState }[] {
  return Object.entries(states ?? {})
    .sort(([a], [b]) => compareRegions(a, b))
    .map(([region, state]) => ({ region, state }))
}

const slug = /^[a-z0-9][a-z0-9-]*$/

/** validateProbeForm mirrors the server: name and region are 1-40 chars of [a-z0-9-], and "local" is reserved. */
export function validateProbeForm(f: { name: string; region: string }): Record<string, string> {
  const e: Record<string, string> = {}
  for (const k of ['name', 'region'] as const) {
    const v = f[k].trim()
    if (v === '') e[k] = `${k === 'name' ? 'Name' : 'Region'} is required`
    else if (v.length > 40) e[k] = 'At most 40 characters'
    else if (!slug.test(v)) e[k] = 'Use lowercase letters, digits and dashes, starting with a letter or digit'
  }
  if (!e.region && f.region.trim() === 'local') e.region = '"local" is reserved for the built-in checker'
  return e
}
