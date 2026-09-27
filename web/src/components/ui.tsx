import { useState, type ReactNode } from 'react'
import { ApiError } from '../api/client'

export function PageHeader({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      {children && <div className="flex flex-wrap items-center gap-2">{children}</div>}
    </div>
  )
}

export function Loading() {
  return <p className="py-8 text-center text-sm text-muted">Loading…</p>
}

/** errorText is the user-facing message for any thrown error. */
export function errorText(err: unknown): string {
  if (err instanceof ApiError) return err.detail
  if (err instanceof Error) return err.message
  return String(err)
}

export function ErrorMessage({ error }: { error: unknown }) {
  if (!error) return null
  return (
    <p role="alert" className="rounded-md border border-down/40 bg-down/10 px-3 py-2 text-sm text-down">
      {errorText(error)}
    </p>
  )
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="card px-6 py-10 text-center text-sm text-muted">{children}</div>
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      className="btn"
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        })
      }}
    >
      {copied ? 'Copied' : label}
    </button>
  )
}

/** fieldError returns a server field error for name (or a client-side one). */
export function fieldError(err: unknown, name: string, local?: Record<string, string>): string | undefined {
  return local?.[name] ?? (err instanceof ApiError ? err.fields[name] : undefined)
}

export function formatTime(iso: string | null | undefined): string {
  return iso ? new Date(iso).toLocaleString() : '—'
}

/** formatDuration renders seconds as e.g. "10m", "1h 30m", "45s". */
export function formatDuration(s: number): string {
  if (s < 60) return `${s}s`
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rest = s % 60
  return [h ? `${h}h` : '', m ? `${m}m` : '', rest ? `${rest}s` : ''].filter(Boolean).join(' ')
}
