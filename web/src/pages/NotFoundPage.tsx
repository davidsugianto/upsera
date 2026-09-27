import { Link } from 'react-router'

export function NotFoundPage() {
  return (
    <div className="mx-auto max-w-md py-16 text-center">
      <h1 className="text-xl font-semibold">Page not found</h1>
      <p className="mt-2 text-sm text-muted">There's nothing at this address.</p>
      <Link to="/" className="link mt-4 inline-block text-sm">
        Go to the dashboard
      </Link>
    </div>
  )
}
