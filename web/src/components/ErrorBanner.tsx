import { useEffect, useState } from 'react'
import { useQueryClient, type Query } from '@tanstack/react-query'
import { ApiError } from '../api/client'

const retryEveryMs = 10_000

const dbDown = (q: Query) => q.state.error instanceof ApiError && q.state.error.status === 503

/**
 * ErrorBanner shows while any query failed because the database is
 * unreachable (503), and retries those queries until they succeed so the
 * page recovers without a reload.
 */
export function ErrorBanner() {
  const qc = useQueryClient()
  const [down, setDown] = useState(false)
  useEffect(() => {
    const cache = qc.getQueryCache()
    const check = () => setDown(cache.getAll().some(dbDown))
    check()
    return cache.subscribe(check)
  }, [qc])
  useEffect(() => {
    if (!down) return
    const timer = window.setInterval(() => void qc.refetchQueries({ predicate: dbDown }), retryEveryMs)
    return () => clearInterval(timer)
  }, [qc, down])
  if (!down) return null
  return (
    <div role="alert" className="border-b border-pend/40 bg-pend/15 px-4 py-2 text-sm">
      The server can't reach its database right now. Monitoring continues; changes and history are unavailable until it
      recovers.
    </div>
  )
}
