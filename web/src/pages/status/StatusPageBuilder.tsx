import type { Monitor } from '../../api/types'
import {
  addItem,
  addSection,
  availableMonitors,
  canMoveItem,
  flatItemCount,
  MAX_TEXT,
  moveItem,
  moveSection,
  removeItem,
  removeSection,
  renameItem,
  renameSection,
  type BuilderSection,
} from '../../lib/statusPageForm'

/** StatusPageBuilder edits a page's sections and the monitors listed under them. */
export function StatusPageBuilder({
  sections,
  monitors,
  errors,
  onChange,
}: {
  sections: BuilderSection[]
  monitors: Monitor[]
  /** Field errors keyed like the API (`items.2.display_name`, `sections.0.name`, ...). */
  errors: Record<string, string>
  onChange: (sections: BuilderSection[]) => void
}) {
  const byId = new Map(monitors.map((m) => [m.id, m]))
  const free = availableMonitors(monitors, sections)
  // Flat position of each section's first item, to match the API's `items.N.*` error keys.
  const starts = sections.map((_, i) => flatItemCount(sections.slice(0, i)))

  return (
    <div className="space-y-4">
      {sections.length === 0 && (
        <p className="text-sm text-muted">No sections yet. Add a section, then choose the monitors to list in it.</p>
      )}
      {sections.map((s, si) => (
        <div key={s.key} className="rounded-md border border-line p-3">
          <div className="flex flex-wrap items-start gap-2">
            <div className="min-w-48 flex-1">
              <input
                className="input"
                aria-label={`Section ${si + 1} name`}
                aria-invalid={errors[`sections.${si}.name`] ? true : undefined}
                placeholder="Section name (optional)"
                maxLength={MAX_TEXT.name}
                value={s.name}
                onChange={(e) => onChange(renameSection(sections, s.key, e.target.value))}
              />
              {errors[`sections.${si}.name`] && <p className="mt-1 text-xs text-down">{errors[`sections.${si}.name`]}</p>}
            </div>
            <div className="flex gap-1.5">
              <button
                type="button"
                className="btn"
                aria-label={`Move section ${si + 1} up`}
                disabled={si === 0}
                onClick={() => onChange(moveSection(sections, s.key, -1))}
              >
                ↑
              </button>
              <button
                type="button"
                className="btn"
                aria-label={`Move section ${si + 1} down`}
                disabled={si === sections.length - 1}
                onClick={() => onChange(moveSection(sections, s.key, 1))}
              >
                ↓
              </button>
              <button
                type="button"
                className="btn-danger"
                aria-label={`Remove section ${si + 1}`}
                title="Removes the section and takes its monitors off the page"
                onClick={() => onChange(removeSection(sections, s.key))}
              >
                Remove section
              </button>
            </div>
          </div>

          {s.items.length === 0 && <p className="mt-3 text-sm text-muted">No monitors in this section yet.</p>}
          <ul className="mt-3 space-y-2">
            {s.items.map((it, ii) => {
              const m = byId.get(it.monitorId)
              const n = (starts[si] ?? 0) + ii
              const err = errors[`items.${n}.display_name`] ?? errors[`items.${n}.monitor_id`]
              return (
                <li key={it.monitorId} className="rounded-md bg-bg p-2">
                  <div className="flex flex-wrap items-start gap-2">
                    <div className="min-w-48 flex-1">
                      <label className="mb-0.5 block text-xs text-muted" htmlFor={`item-${it.monitorId}`}>
                        Public name (shown to everyone) for{' '}
                        <span className="font-medium text-ink">{m?.name ?? `monitor #${it.monitorId}`}</span>
                      </label>
                      <input
                        id={`item-${it.monitorId}`}
                        className="input"
                        aria-invalid={err ? true : undefined}
                        placeholder={m?.name}
                        maxLength={MAX_TEXT.name}
                        value={it.displayName}
                        onChange={(e) => onChange(renameItem(sections, it.monitorId, e.target.value))}
                      />
                      {err && <p className="mt-1 text-xs text-down">{err}</p>}
                    </div>
                    <div className="flex gap-1.5 pt-5">
                      <button
                        type="button"
                        className="btn"
                        aria-label={`Move ${it.displayName || m?.name || 'monitor'} up`}
                        disabled={!canMoveItem(sections, it.monitorId, -1)}
                        onClick={() => onChange(moveItem(sections, it.monitorId, -1))}
                      >
                        ↑
                      </button>
                      <button
                        type="button"
                        className="btn"
                        aria-label={`Move ${it.displayName || m?.name || 'monitor'} down`}
                        disabled={!canMoveItem(sections, it.monitorId, 1)}
                        onClick={() => onChange(moveItem(sections, it.monitorId, 1))}
                      >
                        ↓
                      </button>
                      <button
                        type="button"
                        className="btn"
                        aria-label={`Remove ${it.displayName || m?.name || 'monitor'} from the page`}
                        onClick={() => onChange(removeItem(sections, it.monitorId))}
                      >
                        ×
                      </button>
                    </div>
                  </div>
                </li>
              )
            })}
          </ul>

          <div className="mt-3">
            <select
              className="input sm:max-w-xs"
              aria-label={`Add a monitor to section ${si + 1}`}
              value=""
              disabled={free.length === 0}
              onChange={(e) => {
                const m = byId.get(Number(e.target.value))
                if (m) onChange(addItem(sections, s.key, m.id, m.name.slice(0, MAX_TEXT.name)))
              }}
            >
              <option value="">{free.length === 0 ? 'No more monitors to add' : 'Add a monitor…'}</option>
              {free.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </select>
          </div>
        </div>
      ))}
      <button type="button" className="btn" onClick={() => onChange(addSection(sections))}>
        Add section
      </button>
    </div>
  )
}
