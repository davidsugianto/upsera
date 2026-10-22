import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { useChannels, usePolicies, useSavePolicy } from '../../api/queries'
import type { EscalationPolicy, PolicyRequest } from '../../api/types'
import { MultiCheck, NumberField, TextField } from '../../components/Fields'
import { ErrorMessage, Loading, PageHeader, fieldError } from '../../components/ui'
import { useNumberParam, useTeamId } from '../../hooks/useTeamId'

const MIN_STEPS = 1
const MAX_STEPS = 10
const MIN_DELAY_MINUTES = 10 / 60
const MAX_DELAY_MINUTES = 24 * 60

type StepForm = { channelIds: number[]; delayMinutes: number }

function validate(name: string, steps: StepForm[]): Record<string, string> {
  const errors: Record<string, string> = {}
  if (name.trim() === '') errors.name = 'Name is required'
  if (steps.length < MIN_STEPS || steps.length > MAX_STEPS)
    errors.steps = `A policy needs ${MIN_STEPS}–${MAX_STEPS} steps`
  steps.forEach((s, i) => {
    if (s.channelIds.length < 1 || s.channelIds.length > 10) errors[`steps.${i}.channel_ids`] = 'Choose 1–10 channels'
    if (!(s.delayMinutes >= MIN_DELAY_MINUTES && s.delayMinutes <= MAX_DELAY_MINUTES)) {
      errors[`steps.${i}.delay_s`] = 'Delay must be between 10 seconds and 24 hours'
    }
  })
  return errors
}

/** PolicyFormPage creates or edits an escalation policy. */
export function PolicyFormPage() {
  const t = useTeamId()
  const policyId = useNumberParam('policyId')
  const editing = !Number.isNaN(policyId)
  const policies = usePolicies(t)
  const existing = editing ? policies.data?.find((p) => p.id === policyId) : undefined

  if (editing && policies.isPending) return <Loading />
  if (editing && policies.error) return <ErrorMessage error={policies.error} />
  if (editing && !existing) {
    return (
      <div>
        <PageHeader title="Policy not found" />
        <Link to={`/t/${t}/policies`} className="link">
          Back to escalation policies
        </Link>
      </div>
    )
  }
  return <PolicyEditor key={policyId} team={t} policyId={editing ? policyId : undefined} initial={existing} />
}

function PolicyEditor({ team, policyId, initial }: { team: number; policyId?: number; initial?: EscalationPolicy }) {
  const navigate = useNavigate()
  const channels = useChannels(team)
  const save = useSavePolicy(team)
  const editing = policyId !== undefined
  const [name, setName] = useState(initial?.name ?? '')
  const [steps, setSteps] = useState<StepForm[]>(
    initial?.steps && initial.steps.length > 0
      ? initial.steps.map((s) => ({
          channelIds: s.channel_ids ?? [],
          delayMinutes: s.delay_s / 60,
        }))
      : [{ channelIds: [], delayMinutes: 10 }],
  )
  const [localErrors, setLocalErrors] = useState<Record<string, string>>({})

  const channelOptions = (channels.data ?? []).map((c) => ({
    value: c.id,
    label: c.name,
  }))

  const updateStep = (i: number, patch: Partial<StepForm>) =>
    setSteps((s) => s.map((step, j) => (j === i ? { ...step, ...patch } : step)))

  const moveStep = (i: number, dir: -1 | 1) =>
    setSteps((s) => {
      const j = i + dir
      if (j < 0 || j >= s.length) return s
      const copy = s.slice()
      const tmp = copy[i]!
      copy[i] = copy[j]!
      copy[j] = tmp
      return copy
    })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const errors = validate(name, steps)
    setLocalErrors(errors)
    if (Object.keys(errors).length > 0) return
    const body: PolicyRequest = {
      name: name.trim(),
      steps: steps.map((s) => ({
        channel_ids: s.channelIds,
        delay_s: Math.round(s.delayMinutes * 60),
      })),
    }
    save.mutate({ id: policyId, body }, { onSuccess: () => navigate(`/t/${team}/policies`) })
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title={editing ? 'Edit escalation policy' : 'Add escalation policy'} />
      <form onSubmit={submit} className="space-y-6">
        <ErrorMessage error={save.error} />
        <section className="card p-4">
          <h2 className="mb-4 text-base font-semibold text-balance">Policy</h2>
          <TextField
            label="Name"
            value={name}
            onChange={setName}
            required
            error={fieldError(save.error, 'name', localErrors)}
          />
        </section>
        <section className="card p-4">
          <h2 className="mb-2 text-base font-semibold text-balance">Steps</h2>
          <ol className="divide-y divide-line">
            {steps.map((step, i) => (
              <li key={i} className="space-y-4 py-4 first:pt-2 last:pb-0">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <h3 className="text-sm font-semibold">Step {i + 1}</h3>
                  <div className="flex gap-1">
                    <button
                      type="button"
                      className="btn"
                      aria-label="Move step up"
                      onClick={() => moveStep(i, -1)}
                      disabled={i === 0}
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      className="btn"
                      aria-label="Move step down"
                      onClick={() => moveStep(i, 1)}
                      disabled={i === steps.length - 1}
                    >
                      ↓
                    </button>
                    <button
                      type="button"
                      className="btn-danger"
                      aria-label="Remove step"
                      onClick={() => setSteps((s) => s.filter((_, j) => j !== i))}
                      disabled={steps.length <= MIN_STEPS}
                    >
                      Remove
                    </button>
                  </div>
                </div>
                <MultiCheck
                  label="Channels"
                  options={channelOptions}
                  value={step.channelIds}
                  onChange={(ids) => updateStep(i, { channelIds: ids })}
                  error={localErrors[`steps.${i}.channel_ids`]}
                  empty="No channels yet — create one first."
                />
                <NumberField
                  label="Wait before next step (minutes)"
                  value={step.delayMinutes}
                  onChange={(v) => updateStep(i, { delayMinutes: v })}
                  min={MIN_DELAY_MINUTES}
                  max={MAX_DELAY_MINUTES}
                  step="any"
                  error={localErrors[`steps.${i}.delay_s`]}
                  help="10 seconds to 24 hours; the last step's delay is still sent to the server."
                />
              </li>
            ))}
          </ol>
          <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-line pt-4">
            <button
              type="button"
              className="btn"
              onClick={() => setSteps((s) => [...s, { channelIds: [], delayMinutes: 10 }])}
              disabled={steps.length >= MAX_STEPS}
            >
              Add step
            </button>
            {localErrors.steps && (
              <p role="alert" className="text-xs text-down-ink">
                {localErrors.steps}
              </p>
            )}
          </div>
        </section>
        <div className="sticky bottom-0 z-10 flex justify-end gap-2 border-t border-line bg-bg py-3">
          <Link to={`/t/${team}/policies`} className="btn">
            Cancel
          </Link>
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {editing ? 'Save' : 'Create'}
          </button>
        </div>
      </form>
    </div>
  )
}
