import type { components } from './schema'

type S = components['schemas']

/** degraded: up but slow (a region's recent p95 latency is far above its baseline). */
export type Status = 'up' | 'down' | 'pending' | 'maintenance' | 'degraded'
export type Role = S['MeTeam']['role']
export type MonitorType = S['MonitorBody']['type']
export type ChannelType = S['ChannelBody']['type']

export type MonitorState = S['MonitorStateBody']
/** Monitor as served: state is null until the monitor's first check. */
export type Monitor = Omit<S['MonitorBody'], 'state'> & {
  state: MonitorState | null
}
export type MonitorRequest = S['MonitorRequestBody']
/** Heartbeat: `region` names the probe region that produced it. */
export type Heartbeat = S['HeartbeatBody'] & { region?: string }
export type Alert = S['AlertBody']
export type Channel = S['ChannelBody']
export type ChannelRequest = S['ChannelRequestBody']
export type DockerHost = S['DockerHostBody']
export type DockerHostRequest = S['DockerHostRequestBody']
export type DockerContainer = S['Container']
export type EscalationPolicy = S['PolicyBody']
export type EscalationStep = S['EscalationStepBody']
export type PolicyRequest = S['PolicyRequestBody']
export type MaintenanceWindow = S['MaintenanceWindowBody']
export type MaintenanceRequest = S['MaintenanceWindowRequestBody']
export type Member = S['MemberBody']
export type Token = S['TokenBody']
export type CreatedToken = S['CreateTokenOutputBody']
export type TeamSummary = S['MeTeam']
export type Team = S['TeamBody']
export type Me = S['MeBody']
export type AdminUser = S['AdminUserBody']
export type SmtpSettings = S['SmtpBody']
/** Instance settings as served: smtp is null until a mail server is configured. */
export type Settings = Omit<S['SettingsBody'], 'smtp'> & { smtp: SmtpSettings | null }
/** SettingsUpdate is the PUT body: smtp omitted keeps the mail server, null removes it. */
export type SettingsUpdate = {
  block_private_targets: boolean
  retention_days?: number
  smtp?: SmtpSettings | null
}
export type OverviewItem = S['MonitorOverviewItem']
export type UptimeSummary = S['UptimeSummaryOutputBody']
export type StatusEvent = S['StatusEventBody']
export type StatusPage = S['StatusPageBody']
export type StatusPageRequest = S['StatusPageRequestBody']
export type StatusPageItem = S['StatusPageItemBody']
export type StatusPageItemRequest = S['StatusPageItemRequest']
export type Theme = NonNullable<S['StatusPageRequestBody']['theme']>
export type Incident = S['IncidentBody']
export type IncidentUpdate = S['IncidentUpdateBody']
export type IncidentRequest = S['IncidentRequestBody']
export type IncidentEditRequest = S['IncidentEditRequestBody']
export type IncidentUpdateRequest = S['IncidentUpdateRequestBody']
export type IncidentStatus = S['IncidentBody']['status']
export type IncidentSeverity = S['IncidentBody']['severity']
export type Subscriber = S['SubscriberBody']
export type WebhookSubscriberRequest = S['WebhookSubscriberRequestBody']

export type RegionState = S['RegionStateBody']
export type Region = S['RegionBody']
export type Probe = S['ProbeBody']
/** CreatedProbe is the POST /api/admin/probes answer; the token is shown once. */
export type CreatedProbe = S['CreateProbeOutputBody']
export type ProbeRegistration = S['ProbeTokenBody']

/** SSE `monitor` event: a monitor changed state. */
export type MonitorEvent = {
  monitor_id: number
  from: Status
  to: Status
  at: string
  since: string
  message: string
  flapping: boolean
}

/** SSE `monitors_changed` event: a monitor was created, updated or deleted. */
export type MonitorsChangedEvent = { monitor_id: number; deleted: boolean }

/** SSE `alert` events carry an Alert. */
export type AlertFilter = { state: 'open' | 'resolved' | 'all'; monitorId?: number }

export type SLO = S['SloBody']
export type SLOStatus = S['SloStatusBody']
export type SLOPoint = S['SloPointBody']
/** SLODetail is the GET/PUT …/slo answer: the SLO, its status and its daily series. */
export type SLODetail = S['SloOutputBody']
/** SLOInput is the PUT …/slo body; every field is sent (the server defaults are 30 / 14.4 / 6). */
export type SLOInput = Omit<S['SloInputBody'], '$schema'>
export type SLOFiring = S['SloStatusBody']['firing']
export type SLAReport = S['SlaReportOutputBody']
export type SLARow = S['SlaRowBody']
