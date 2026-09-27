const key = 'upsera.team'

export function lastTeam(): number | null {
  const v = Number(localStorage.getItem(key))
  return Number.isInteger(v) && v > 0 ? v : null
}

export function rememberTeam(id: number) {
  localStorage.setItem(key, String(id))
}
