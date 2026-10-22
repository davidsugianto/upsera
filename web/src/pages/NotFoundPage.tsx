import { Link } from 'react-router'

export function NotFoundPage() {
  return (
    <div className="mx-auto w-full max-w-sm px-4 py-16">
      <div className="card space-y-2 px-6 py-10 text-center">
        <h1 className="text-xl font-semibold tracking-tight">Page not found</h1>
        <p className="text-sm text-muted">There's nothing at this address.</p>
        <Link to="/" className="link inline-block pt-2 text-sm">
          Go to the dashboard
        </Link>
      </div>
    </div>
  )
}
