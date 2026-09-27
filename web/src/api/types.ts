import type { components } from './schema'

type S = components['schemas']

export type Status = 'up' | 'down' | 'pending' | 'maintenance'
export type Role = S['MeTeam']['role']
export type MonitorType = S['MonitorBody']['type']
export type ChannelType = S['ChannelBody']['type']

export type MonitorState = S['MonitorStateBody']
/** Monitor as served: state is null until the monitor's first check. */
export type Monitor = Omit<S['MonitorBody'], 'state'> & { state: MonitorState | null }
export type MonitorRequest = S['MonitorRequestBody']
export type Heartbeat = S['HeartbeatBody']
export type Alert = S['AlertBody']
export type Channel = S['ChannelBody']
export type ChannelRequest = S['ChannelRequestBody']
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
export type Settings = S['SettingsBody']
export type OverviewItem = S['MonitorOverviewItem']
export type UptimeSummary = S['UptimeSummaryOutputBody']
export type StatusEvent = S['StatusEventBody']

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
