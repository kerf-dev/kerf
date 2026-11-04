import { CompassIcon } from 'lucide-react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/states'
import { Button } from '@/components/ui/button'

export default function NotFoundPage() {
  return (
    <EmptyState
      icon={CompassIcon}
      title="Page not found"
      description="The page you are looking for does not exist or has moved."
      action={
        <Button asChild size="sm">
          <Link to="/">Back to overview</Link>
        </Button>
      }
      className="py-24"
    />
  )
}
