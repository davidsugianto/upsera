export type Theme = 'light' | 'dark'

const key = 'upsera.theme'

export function storedTheme(): Theme {
  return localStorage.getItem(key) === 'light' ? 'light' : 'dark'
}

export function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = t
}

export function saveTheme(t: Theme) {
  localStorage.setItem(key, t)
  applyTheme(t)
}
