/** Theme is a resolved appearance; ThemeChoice also allows following the OS. */
export type Theme = 'light' | 'dark'
export type ThemeChoice = Theme | 'system'

const key = 'upsera.theme'

/** storedChoice is what the user picked; with nothing saved the dashboard follows the OS. */
export function storedChoice(): ThemeChoice {
  try {
    const v = localStorage.getItem(key)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

/** resolveTheme turns a choice into the light or dark appearance actually shown. */
export function resolveTheme(c: ThemeChoice): Theme {
  if (c !== 'system') return c
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/** storedTheme is the appearance currently shown (the saved choice, or the OS preference). */
export function storedTheme(): Theme {
  return resolveTheme(storedChoice())
}

/**
 * applyChoice sets data-theme for an explicit light or dark choice and removes it for
 * "system", so the CSS prefers-color-scheme rules take over without any inline script.
 */
export function applyChoice(c: ThemeChoice) {
  if (c === 'system') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = c
}

export function saveChoice(c: ThemeChoice) {
  try {
    if (c === 'system') localStorage.removeItem(key)
    else localStorage.setItem(key, c)
  } catch {
    // Storage can be blocked; the choice still applies for this page view.
  }
  applyChoice(c)
}

/** applyTheme and saveTheme keep the two-state API the user menu already calls. */
export function applyTheme(t: Theme) {
  applyChoice(t)
}

export function saveTheme(t: Theme) {
  saveChoice(t)
}
