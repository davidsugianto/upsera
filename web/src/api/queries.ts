import { useMutation, useQuery, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { ApiError, client, setCsrf, unwrap } from './client'
import type {
  AdminUser,
  Alert,
  AlertFilter,
  Channel,
  ChannelRequest,
  CreatedToken,
  EscalationPolicy,
  Heartbeat,
  MaintenanceRequest,
  MaintenanceWindow,
  Me,
  Member,
  Monitor,
  MonitorRequest,
  OverviewItem,
  PolicyRequest,
  Role,
  Settings,
  StatusEvent,
  Team,
  Token,
  UptimeSummary,
} from './types'

/** Query keys. The second element of every team-scoped key is the team id. */
export const keys = {
  me: ['me'] as const,
  setup: ['setup'] as const,
  monitors: (t: number) => ['monitors', t] as const,
  monitor: (t: number, id: number) => ['monitor', t, id] as const,
  overview: (t: number) => ['overview', t] as const,
  heartbeats: (t: number, id: number, range: string) => ['heartbeats', t, id, range] as const,
  heartbeatsAll: (t: number, id: number) => ['heartbeats', t, id] as const,
  uptimeSummary: (t: number, id: number) => ['uptimeSummary', t, id] as const,
  statusEvents: (t: number, id: number) => ['statusEvents', t, id] as const,
  alerts: (t: number, filter: AlertFilter) => ['alerts', t, filter] as const,
  alertsAll: (t: number) => ['alerts', t] as const,
  channels: (t: number) => ['channels', t] as const,
  policies: (t: number) => ['policies', t] as const,
  maintenance: (t: number) => ['maintenance', t] as const,
  members: (t: number) => ['members', t] as const,
  tokens: (t: number) => ['tokens', t] as const,
  adminUsers: ['adminUsers'] as const,
  adminSettings: ['adminSettings'] as const,
}

// ---- session ----

/** useMe returns the signed-in user, or null when signed out (401). */
export function useMe() {
  return useQuery({
    queryKey: keys.me,
    queryFn: async (): Promise<Me | null> => {
      try {
        const me = await unwrap(client.GET('/api/auth/me'))
        setCsrf(me.csrf_token)
        return me
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null
        throw e
      }
    },
    staleTime: 60_000,
  })
}

export function useSetupStatus() {
  return useQuery({
    queryKey: keys.setup,
    queryFn: () => unwrap(client.GET('/api/setup')),
    staleTime: Infinity,
  })
}

function useSessionMutation<V>(fn: (v: V) => Promise<Me>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: (me) => {
      setCsrf(me.csrf_token)
      qc.setQueryData(keys.me, me)
      qc.setQueryData(keys.setup, { needed: false })
    },
  })
}

export function useLogin() {
  return useSessionMutation((body: { email: string; password: string }) =>
    unwrap(client.POST('/api/auth/login', { body })),
  )
}

export function useSetup() {
  return useSessionMutation((body: { email: string; name: string; password: string; team_name: string }) =>
    unwrap(client.POST('/api/setup', { body })),
  )
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(client.POST('/api/auth/logout')),
    onSettled: () => {
      qc.clear()
      qc.setQueryData(keys.me, null)
    },
  })
}

export function useCreateTeam() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string): Promise<Team> => unwrap(client.POST('/api/teams', { body: { name } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.me }),
  })
}

// ---- helpers ----

const teamPath = (teamID: number) => ({ params: { path: { teamID } } })

function useInvalidatingMutation<V, R>(fn: (v: V) => Promise<R>, invalidate: (v: V) => QueryKey[]) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: (_r, v) => Promise.all(invalidate(v).map((queryKey) => qc.invalidateQueries({ queryKey }))),
  })
}

// ---- monitors ----

export function useMonitors(t: number) {
  return useQuery({
    queryKey: keys.monitors(t),
    queryFn: async (): Promise<Monitor[]> =>
      ((await unwrap(client.GET('/api/teams/{teamID}/monitors', teamPath(t)))).monitors ?? []) as Monitor[],
  })
}

export function useMonitor(t: number, id: number, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: keys.monitor(t, id),
    queryFn: async (): Promise<Monitor> =>
      (await unwrap(
        client.GET('/api/teams/{teamID}/monitors/{monitorID}', { params: { path: { teamID: t, monitorID: id } } }),
      )) as Monitor,
    enabled: opts.enabled ?? true,
  })
}

export function useOverview(t: number) {
  return useQuery({
    queryKey: keys.overview(t),
    queryFn: async (): Promise<OverviewItem[]> =>
      (await unwrap(client.GET('/api/teams/{teamID}/monitor-overview', teamPath(t)))).monitors ?? [],
    refetchInterval: 60_000,
  })
}

/**
 * useHeartbeats loads a monitor's heartbeats, newest first. range names the
 * cache entry (e.g. 'last100', '1h'); sinceMs, when set, bounds how far back.
 */
export function useHeartbeats(t: number, id: number, range: string, opts: { limit: number; sinceMs?: number }) {
  return useQuery({
    queryKey: keys.heartbeats(t, id, range),
    queryFn: async (): Promise<Heartbeat[]> => {
      const query: { limit: number; since?: string } = { limit: opts.limit }
      if (opts.sinceMs) query.since = new Date(Date.now() - opts.sinceMs).toISOString()
      const r = await unwrap(
        client.GET('/api/teams/{teamID}/monitors/{monitorID}/heartbeats', {
          params: { path: { teamID: t, monitorID: id }, query },
        }),
      )
      return r.heartbeats ?? []
    },
    refetchInterval: 60_000,
  })
}

export function useUptimeSummary(t: number, id: number) {
  return useQuery({
    queryKey: keys.uptimeSummary(t, id),
    queryFn: (): Promise<UptimeSummary> =>
      unwrap(
        client.GET('/api/teams/{teamID}/monitors/{monitorID}/uptime-summary', {
          params: { path: { teamID: t, monitorID: id } },
        }),
      ),
  })
}

export function useStatusEvents(t: number, id: number) {
  return useQuery({
    queryKey: keys.statusEvents(t, id),
    queryFn: async (): Promise<StatusEvent[]> =>
      (
        await unwrap(
          client.GET('/api/teams/{teamID}/monitors/{monitorID}/events', {
            params: { path: { teamID: t, monitorID: id }, query: { limit: 50 } },
          }),
        )
      ).events ?? [],
  })
}

function monitorKeys(t: number, id?: number): QueryKey[] {
  const k: QueryKey[] = [keys.monitors(t), keys.overview(t)]
  if (id !== undefined) k.push(keys.monitor(t, id))
  return k
}

/** useSaveMonitor creates (no id) or updates (id) a monitor. */
export function useSaveMonitor(t: number) {
  return useInvalidatingMutation(
    async ({ id, body }: { id?: number; body: MonitorRequest }): Promise<Monitor> =>
      (id === undefined
        ? await unwrap(client.POST('/api/teams/{teamID}/monitors', { ...teamPath(t), body }))
        : await unwrap(
            client.PUT('/api/teams/{teamID}/monitors/{monitorID}', {
              params: { path: { teamID: t, monitorID: id } },
              body,
            }),
          )) as Monitor,
    ({ id }) => monitorKeys(t, id),
  )
}

export function useDeleteMonitor(t: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number): Promise<void> =>
      unwrap(client.DELETE('/api/teams/{teamID}/monitors/{monitorID}', { params: { path: { teamID: t, monitorID: id } } })),
    onSuccess: (_r, id) => {
      qc.removeQueries({ queryKey: keys.monitor(t, id) })
      return Promise.all(monitorKeys(t).map((queryKey) => qc.invalidateQueries({ queryKey })))
    },
  })
}

// ---- alerts ----

export function useAlerts(t: number, filter: AlertFilter) {
  return useQuery({
    queryKey: keys.alerts(t, filter),
    queryFn: async (): Promise<Alert[]> => {
      const query: { state: AlertFilter['state']; monitor_id?: number; limit: number } = {
        state: filter.state,
        limit: 100,
      }
      if (filter.monitorId !== undefined) query.monitor_id = filter.monitorId
      return (await unwrap(client.GET('/api/teams/{teamID}/alerts', { params: { path: { teamID: t }, query } }))).alerts ?? []
    },
  })
}

export function useAcknowledge(t: number) {
  return useMutation({
    mutationFn: (alertID: string): Promise<Alert> =>
      unwrap(client.POST('/api/teams/{teamID}/alerts/{alertID}/acknowledge', { params: { path: { teamID: t, alertID } } })),
  })
}

// ---- channels ----

export function useChannels(t: number) {
  return useQuery({
    queryKey: keys.channels(t),
    queryFn: async (): Promise<Channel[]> =>
      (await unwrap(client.GET('/api/teams/{teamID}/channels', teamPath(t)))).channels ?? [],
  })
}

export function useSaveChannel(t: number) {
  return useInvalidatingMutation(
    ({ id, body }: { id?: number; body: ChannelRequest }): Promise<Channel> =>
      id === undefined
        ? unwrap(client.POST('/api/teams/{teamID}/channels', { ...teamPath(t), body }))
        : unwrap(
            client.PUT('/api/teams/{teamID}/channels/{channelID}', {
              params: { path: { teamID: t, channelID: id } },
              body,
            }),
          ),
    () => [keys.channels(t)],
  )
}

export function useDeleteChannel(t: number) {
  return useInvalidatingMutation(
    (id: number): Promise<void> =>
      unwrap(client.DELETE('/api/teams/{teamID}/channels/{channelID}', { params: { path: { teamID: t, channelID: id } } })),
    () => [keys.channels(t)],
  )
}

export function useTestChannel(t: number) {
  return useMutation({
    mutationFn: (id: number) =>
      unwrap(client.POST('/api/teams/{teamID}/channels/{channelID}/test', { params: { path: { teamID: t, channelID: id } } })),
  })
}

// ---- escalation policies ----

export function usePolicies(t: number) {
  return useQuery({
    queryKey: keys.policies(t),
    queryFn: async (): Promise<EscalationPolicy[]> =>
      (await unwrap(client.GET('/api/teams/{teamID}/escalation-policies', teamPath(t)))).escalation_policies ?? [],
  })
}

export function useSavePolicy(t: number) {
  return useInvalidatingMutation(
    ({ id, body }: { id?: number; body: PolicyRequest }): Promise<EscalationPolicy> =>
      id === undefined
        ? unwrap(client.POST('/api/teams/{teamID}/escalation-policies', { ...teamPath(t), body }))
        : unwrap(
            client.PUT('/api/teams/{teamID}/escalation-policies/{policyID}', {
              params: { path: { teamID: t, policyID: id } },
              body,
            }),
          ),
    () => [keys.policies(t)],
  )
}

export function useDeletePolicy(t: number) {
  return useInvalidatingMutation(
    (id: number): Promise<void> =>
      unwrap(
        client.DELETE('/api/teams/{teamID}/escalation-policies/{policyID}', { params: { path: { teamID: t, policyID: id } } }),
      ),
    () => [keys.policies(t)],
  )
}

// ---- maintenance windows ----

export function useMaintenanceWindows(t: number) {
  return useQuery({
    queryKey: keys.maintenance(t),
    queryFn: async (): Promise<MaintenanceWindow[]> =>
      (await unwrap(client.GET('/api/teams/{teamID}/maintenance-windows', teamPath(t)))).maintenance_windows ?? [],
  })
}

export function useSaveMaintenance(t: number) {
  return useInvalidatingMutation(
    ({ id, body }: { id?: number; body: MaintenanceRequest }): Promise<MaintenanceWindow> =>
      id === undefined
        ? unwrap(client.POST('/api/teams/{teamID}/maintenance-windows', { ...teamPath(t), body }))
        : unwrap(
            client.PUT('/api/teams/{teamID}/maintenance-windows/{windowID}', {
              params: { path: { teamID: t, windowID: id } },
              body,
            }),
          ),
    () => [keys.maintenance(t)],
  )
}

export function useDeleteMaintenance(t: number) {
  return useInvalidatingMutation(
    (id: number): Promise<void> =>
      unwrap(
        client.DELETE('/api/teams/{teamID}/maintenance-windows/{windowID}', { params: { path: { teamID: t, windowID: id } } }),
      ),
    () => [keys.maintenance(t)],
  )
}

// ---- members ----

export function useMembers(t: number) {
  return useQuery({
    queryKey: keys.members(t),
    queryFn: async (): Promise<Member[]> =>
      (await unwrap(client.GET('/api/teams/{teamID}/members', teamPath(t)))).members ?? [],
  })
}

export function useAddMember(t: number) {
  return useInvalidatingMutation(
    (body: { email: string; role: Role }): Promise<Member> =>
      unwrap(client.POST('/api/teams/{teamID}/members', { ...teamPath(t), body })),
    () => [keys.members(t)],
  )
}

export function useUpdateMember(t: number) {
  return useInvalidatingMutation(
    ({ userID, role }: { userID: number; role: Role }): Promise<Member> =>
      unwrap(
        client.PUT('/api/teams/{teamID}/members/{userID}', { params: { path: { teamID: t, userID } }, body: { role } }),
      ),
    () => [keys.members(t), keys.me],
  )
}

export function useRemoveMember(t: number) {
  return useInvalidatingMutation(
    (userID: number): Promise<void> =>
      unwrap(client.DELETE('/api/teams/{teamID}/members/{userID}', { params: { path: { teamID: t, userID } } })),
    () => [keys.members(t), keys.me],
  )
}

// ---- tokens ----

export function useTokens(t: number) {
  return useQuery({
    queryKey: keys.tokens(t),
    queryFn: async (): Promise<Token[]> =>
      (await unwrap(client.GET('/api/teams/{teamID}/tokens', teamPath(t)))).tokens ?? [],
  })
}

export function useCreateToken(t: number) {
  return useInvalidatingMutation(
    (body: { name: string; scope: 'read' | 'write'; expires_at?: string }): Promise<CreatedToken> =>
      unwrap(client.POST('/api/teams/{teamID}/tokens', { ...teamPath(t), body })),
    () => [keys.tokens(t)],
  )
}

export function useRevokeToken(t: number) {
  return useInvalidatingMutation(
    (tokenID: number): Promise<void> =>
      unwrap(client.DELETE('/api/teams/{teamID}/tokens/{tokenID}', { params: { path: { teamID: t, tokenID } } })),
    () => [keys.tokens(t)],
  )
}

// ---- instance admin ----

export function useAdminUsers(enabled = true) {
  return useQuery({
    queryKey: keys.adminUsers,
    queryFn: async (): Promise<AdminUser[]> => (await unwrap(client.GET('/api/admin/users'))).users ?? [],
    enabled,
  })
}

export function useCreateAdminUser() {
  return useInvalidatingMutation(
    (body: { email: string; name: string; password: string; is_admin: boolean }): Promise<AdminUser> =>
      unwrap(client.POST('/api/admin/users', { body })),
    () => [keys.adminUsers],
  )
}

export function useAdminSettings(enabled = true) {
  return useQuery({
    queryKey: keys.adminSettings,
    queryFn: (): Promise<Settings> => unwrap(client.GET('/api/admin/settings')),
    enabled,
  })
}

export function useSaveAdminSettings() {
  return useInvalidatingMutation(
    (body: { block_private_targets: boolean; retention_days?: number }): Promise<Settings> =>
      unwrap(client.PUT('/api/admin/settings', { body })),
    () => [keys.adminSettings],
  )
}
