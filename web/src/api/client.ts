import createClient, { type Middleware } from 'openapi-fetch'
import { QueryClient } from '@tanstack/react-query'
import type { paths } from './schema'

/** ApiError is a failed API call, built from huma's problem JSON. */
export class ApiError extends Error {
  status: number
  detail: string
  /** Field errors keyed by location without the `body.` prefix (e.g. `config.url`). */
  fields: Record<string, string>

  constructor(status: number, detail: string, fields: Record<string, string> = {}) {
    super(detail)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
    this.fields = fields
  }

  static from(status: number, body: unknown): ApiError {
    const p = (body && typeof body === 'object' ? body : {}) as {
      title?: string
      detail?: string
      errors?: { message?: string; location?: string }[] | null
    }
    const fields: Record<string, string> = {}
    for (const e of p.errors ?? []) {
      if (e.location && e.message) fields[e.location.replace(/^body\./, '')] = e.message
    }
    let detail = p.detail || p.errors?.find((e) => e.message)?.message || p.title || ''
    if (!detail && status === 503) detail = "The server can't reach its database right now."
    if (!detail) detail = `Request failed (${status})`
    return new ApiError(status, detail, fields)
  }
}

let csrf = ''

/** setCsrf stores the session's CSRF token for non-GET requests. */
export function setCsrf(t: string) {
  csrf = t
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: (n, err) => !(err instanceof ApiError && err.status < 500) && n < 3,
    },
  },
})

const middleware: Middleware = {
  onRequest({ request }) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      request.headers.set('X-CSRF-Token', csrf)
    }
    return request
  },
  onResponse({ request, response }) {
    const path = new URL(request.url).pathname
    if (response.status === 401 && path !== '/api/auth/me' && path !== '/api/auth/login') {
      queryClient.setQueryData(['me'], null)
    }
    return response
  },
}

export const client = createClient<paths>({ baseUrl: '' })
client.use(middleware)

/** unwrap resolves an openapi-fetch call to its data or throws an ApiError. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const { data, error, response } = await p
  if (response.ok) {
    return (response.status === 204 ? undefined : data) as T
  }
  throw ApiError.from(response.status, error)
}
