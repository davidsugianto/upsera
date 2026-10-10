import type { DockerHost, DockerHostRequest } from '../api/types'

/** REDACTED is what the API returns for a TLS field that is set; sending it back keeps the stored value. */
export const REDACTED = '********'

export type DockerHostForm = { name: string; endpoint: string; ca: string; cert: string; key: string }

export const emptyDockerHostForm: DockerHostForm = {
  name: '',
  endpoint: '',
  ca: '',
  cert: '',
  key: '',
}

export function hostToForm(h: DockerHost): DockerHostForm {
  return { name: h.name, endpoint: h.endpoint, ca: h.tls.ca, cert: h.tls.cert, key: h.tls.key }
}

/**
 * formToBody builds the request. On create, empty TLS fields are omitted. On update they are sent as
 * "" (which clears the stored value) and REDACTED is sent unchanged (which keeps it).
 */
export function formToBody(f: DockerHostForm, editing: boolean): DockerHostRequest {
  const body: DockerHostRequest = { name: f.name.trim(), endpoint: f.endpoint.trim() }
  const tls: NonNullable<DockerHostRequest['tls']> = {}
  for (const k of ['ca', 'cert', 'key'] as const) {
    const v = f[k].trim()
    if (v !== '' || editing) tls[k] = v
  }
  if (Object.keys(tls).length > 0) body.tls = tls
  return body
}

/** insecureWarning explains when a TCP endpoint talks to the Docker API without TLS; '' when it does not. */
export function insecureWarning(f: Pick<DockerHostForm, 'endpoint' | 'ca' | 'cert' | 'key'>): string {
  const e = f.endpoint.trim().toLowerCase()
  const plain = e.startsWith('http://') || (e.startsWith('tcp://') && f.ca === '' && f.cert === '' && f.key === '')
  if (!plain) return ''
  return 'No TLS: traffic to this Docker host is unencrypted. That is fine for the bundled socket proxy on a private network; use TLS for anything remote.'
}
