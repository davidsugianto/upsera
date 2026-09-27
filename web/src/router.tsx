import { createBrowserRouter } from 'react-router'
import { HomePage } from './pages/HomePage'
import { SetupPage } from './pages/SetupPage'
import { LoginPage } from './pages/LoginPage'
import { NoTeamPage } from './pages/NoTeamPage'
import { AdminPage } from './pages/AdminPage'
import { NotFoundPage } from './pages/NotFoundPage'
import { TeamLayout } from './pages/TeamLayout'
import { MonitorsPage } from './pages/monitors/MonitorsPage'
import { MonitorFormPage } from './pages/monitors/MonitorFormPage'
import { MonitorDetailPage } from './pages/monitors/MonitorDetailPage'
import { ChannelsPage } from './pages/channels/ChannelsPage'
import { ChannelFormPage } from './pages/channels/ChannelFormPage'
import { PoliciesPage } from './pages/policies/PoliciesPage'
import { PolicyFormPage } from './pages/policies/PolicyFormPage'
import { MaintenancePage } from './pages/maintenance/MaintenancePage'
import { MaintenanceFormPage } from './pages/maintenance/MaintenanceFormPage'
import { MembersPage } from './pages/MembersPage'
import { TokensPage } from './pages/TokensPage'

export const router = createBrowserRouter([
  { path: '/', element: <HomePage /> },
  { path: '/setup', element: <SetupPage /> },
  { path: '/login', element: <LoginPage /> },
  { path: '/no-team', element: <NoTeamPage /> },
  { path: '/admin', element: <AdminPage /> },
  {
    path: '/t/:teamId',
    element: <TeamLayout />,
    children: [
      { index: true, element: <MonitorsPage /> },
      { path: 'monitors/new', element: <MonitorFormPage /> },
      { path: 'monitors/:monitorId', element: <MonitorDetailPage /> },
      { path: 'monitors/:monitorId/edit', element: <MonitorFormPage /> },
      { path: 'channels', element: <ChannelsPage /> },
      { path: 'channels/new', element: <ChannelFormPage /> },
      { path: 'channels/:channelId/edit', element: <ChannelFormPage /> },
      { path: 'policies', element: <PoliciesPage /> },
      { path: 'policies/new', element: <PolicyFormPage /> },
      { path: 'policies/:policyId/edit', element: <PolicyFormPage /> },
      { path: 'maintenance', element: <MaintenancePage /> },
      { path: 'maintenance/new', element: <MaintenanceFormPage /> },
      { path: 'maintenance/:windowId/edit', element: <MaintenanceFormPage /> },
      { path: 'members', element: <MembersPage /> },
      { path: 'tokens', element: <TokensPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
])
