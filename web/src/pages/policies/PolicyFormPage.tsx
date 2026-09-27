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
  if (steps.length < MIN_STEPS || steps.length > MAX_STEPS) errors.steps = `A policy needs ${MIN_STEPS}–${MAX_STEPS} steps`
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

function PolicyEditor({
  team,
  policyId,
  initial,
}: {
  team: number
  policyId?: number
  initial?: EscalationPolicy
}) {
  const navigate = useNavigate()
  const channels = useChannels(team)
  const save = useSavePolicy(team)
  const editing = policyId !== undefined
  const [name, setName] = useState(initial?.name ?? '')
  const [steps, setSteps] = useState<StepForm[]>(
    initial?.steps && initial.steps.length > 0
      ? initial.steps.map((s) => ({ channelIds: s.channel_ids ?? [], delayMinutes: s.delay_s / 60 }))
      : [{ channelIds: [], delayMinutes: 10 }],
  )
  const [localErrors, setLocalErrors] = useState<Record<string, string>>({})

  const channelOptions = (channels.data ?? []).map((c) => ({ value: c.id, label: c.name }))

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
      steps: steps.map((s) => ({ channel_ids: s.channelIds, delay_s: Math.round(s.delayMinutes * 60) })),
    }
    save.mutate({ id: policyId, body }, { onSuccess: () => navigate(`/t/${team}/policies`) })
  }

  return (
    <div className="max-w-2xl">
      <PageHeader title={editing ? 'Edit escalation policy' : 'Add escalation policy'} />
      <form onSubmit={submit} className="space-y-4">
        <div className="card space-y-4 p-5">
          <ErrorMessage error={save.error} />
          <TextField
            label="Name"
            value={name}
            onChange={setName}
            required
            error={fieldError(save.error, 'name', localErrors)}
          />
        </div>
        <div className="space-y-3">
          {steps.map((step, i) => (
            <div key={i} className="card space-y-3 p-4">
              <div className="flex items-center justify-between">
                <h3 className="text-sm font-medium">Step {i + 1}</h3>
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
            </div>
          ))}
        </div>
        <div className="flex items-center gap-3">
          <button
            type="button"
            className="btn"
            onClick={() => setSteps((s) => [...s, { channelIds: [], delayMinutes: 10 }])}
            disabled={steps.length >= MAX_STEPS}
          >
            Add step
          </button>
          {localErrors.steps && (
            <p role="alert" className="text-xs text-down">
              {localErrors.steps}
            </p>
          )}
        </div>
        <div className="flex gap-2">
          <button type="submit" className="btn-primary" disabled={save.isPending}>
            {editing ? 'Save' : 'Create'}
          </button>
          <Link to={`/t/${team}/policies`} className="btn">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  )
}
