import { useId, useState, type ReactNode } from 'react'
import type { HeaderRow } from '../lib/monitorForm'

type Base = { label: string; error?: string; help?: ReactNode; className?: string }

function Field({ id, label, error, help, className = '', children }: Base & { id: string; children: ReactNode }) {
  return (
    <div className={className}>
      <label htmlFor={id} className="mb-1 block text-sm font-medium">
        {label}
      </label>
      {children}
      {help && !error && <p className="mt-1 text-xs text-muted">{help}</p>}
      {error && <p className="mt-1 text-xs text-down">{error}</p>}
    </div>
  )
}

export function TextField({
  value,
  onChange,
  type = 'text',
  placeholder,
  required,
  autoComplete,
  multiline,
  ...base
}: Base & {
  value: string
  onChange: (v: string) => void
  type?: 'text' | 'password' | 'email' | 'url' | 'date' | 'datetime-local'
  placeholder?: string
  required?: boolean
  autoComplete?: string
  multiline?: boolean
}) {
  const id = useId()
  return (
    <Field id={id} {...base}>
      {multiline ? (
        <textarea
          id={id}
          className="input min-h-24 font-mono"
          value={value}
          placeholder={placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : (
        <input
          id={id}
          className="input"
          type={type}
          value={value}
          placeholder={placeholder}
          required={required}
          autoComplete={autoComplete}
          aria-invalid={base.error ? true : undefined}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </Field>
  )
}

export function NumberField({
  value,
  onChange,
  min,
  max,
  step,
  ...base
}: Base & { value: number; onChange: (v: number) => void; min?: number; max?: number; step?: number | 'any' }) {
  const id = useId()
  return (
    <Field id={id} {...base}>
      <input
        id={id}
        className="input"
        type="number"
        value={Number.isNaN(value) ? '' : value}
        min={min}
        max={max}
        step={step}
        aria-invalid={base.error ? true : undefined}
        onChange={(e) => onChange(e.target.value === '' ? NaN : Number(e.target.value))}
      />
    </Field>
  )
}

export function Select({
  value,
  onChange,
  options,
  disabled,
  ...base
}: Base & {
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
  disabled?: boolean
}) {
  const id = useId()
  return (
    <Field id={id} {...base}>
      <select id={id} className="input" value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </Field>
  )
}

export function Checkbox({
  label,
  checked,
  onChange,
  help,
}: {
  label: ReactNode
  checked: boolean
  onChange: (v: boolean) => void
  help?: ReactNode
}) {
  return (
    <label className="flex items-start gap-2 text-sm">
      <input
        type="checkbox"
        className="mt-0.5 accent-[#C2185B]"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span>
        {label}
        {help && <span className="block text-xs text-muted">{help}</span>}
      </span>
    </label>
  )
}

/** TagInput edits a list of short strings; Enter or comma adds the typed value. */
export function TagInput({
  value,
  onChange,
  placeholder = 'Add…',
  ...base
}: Base & { value: string[]; onChange: (v: string[]) => void; placeholder?: string }) {
  const id = useId()
  const [draft, setDraft] = useState('')
  const add = () => {
    const v = draft.trim()
    if (v && !value.includes(v)) onChange([...value, v])
    setDraft('')
  }
  return (
    <Field id={id} {...base}>
      <div className="input flex flex-wrap items-center gap-1.5">
        {value.map((t) => (
          <span key={t} className="inline-flex items-center gap-1 rounded bg-line px-1.5 py-0.5 text-xs">
            {t}
            <button
              type="button"
              className="text-muted hover:text-ink"
              aria-label={`Remove ${t}`}
              onClick={() => onChange(value.filter((x) => x !== t))}
            >
              ×
            </button>
          </span>
        ))}
        <input
          id={id}
          className="min-w-24 flex-1 bg-transparent outline-none"
          value={draft}
          placeholder={placeholder}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={add}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ',') {
              e.preventDefault()
              add()
            } else if (e.key === 'Backspace' && draft === '' && value.length > 0) {
              onChange(value.slice(0, -1))
            }
          }}
        />
      </div>
    </Field>
  )
}

/** MultiCheck is a checkbox list of numeric ids. */
export function MultiCheck({
  label,
  options,
  value,
  onChange,
  error,
  help,
  empty = 'Nothing to choose from yet.',
}: {
  label: string
  options: { value: number; label: ReactNode }[]
  value: number[]
  onChange: (v: number[]) => void
  error?: string
  help?: ReactNode
  empty?: ReactNode
}) {
  return (
    <fieldset>
      <legend className="mb-1 block text-sm font-medium">{label}</legend>
      {options.length === 0 ? (
        <p className="text-sm text-muted">{empty}</p>
      ) : (
        <div className="grid max-h-56 gap-1.5 overflow-y-auto rounded-md border border-line p-2 sm:grid-cols-2">
          {options.map((o) => (
            <Checkbox
              key={o.value}
              label={o.label}
              checked={value.includes(o.value)}
              onChange={(on) => onChange(on ? [...value, o.value] : value.filter((v) => v !== o.value))}
            />
          ))}
        </div>
      )}
      {help && !error && <p className="mt-1 text-xs text-muted">{help}</p>}
      {error && <p className="mt-1 text-xs text-down">{error}</p>}
    </fieldset>
  )
}

/** KeyValueEditor edits header-like key/value rows. */
export function KeyValueEditor({
  label,
  value,
  onChange,
  error,
  help,
}: {
  label: string
  value: HeaderRow[]
  onChange: (v: HeaderRow[]) => void
  error?: string
  help?: ReactNode
}) {
  const set = (i: number, row: HeaderRow) => onChange(value.map((r, j) => (j === i ? row : r)))
  return (
    <fieldset>
      <legend className="mb-1 block text-sm font-medium">{label}</legend>
      <div className="space-y-1.5">
        {value.map((r, i) => (
          <div key={i} className="flex gap-1.5">
            <input
              className="input"
              placeholder="Name"
              aria-label="Header name"
              value={r.key}
              onChange={(e) => set(i, { ...r, key: e.target.value })}
            />
            <input
              className="input"
              placeholder="Value"
              aria-label="Header value"
              value={r.value}
              onChange={(e) => set(i, { ...r, value: e.target.value })}
            />
            <button
              type="button"
              className="btn"
              aria-label="Remove header"
              onClick={() => onChange(value.filter((_, j) => j !== i))}
            >
              ×
            </button>
          </div>
        ))}
        <button type="button" className="btn" onClick={() => onChange([...value, { key: '', value: '' }])}>
          Add header
        </button>
      </div>
      {help && !error && <p className="mt-1 text-xs text-muted">{help}</p>}
      {error && <p className="mt-1 text-xs text-down">{error}</p>}
    </fieldset>
  )
}
