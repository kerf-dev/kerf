import { ActivityIcon } from 'lucide-react'
import { Link } from 'react-router'
import { ConfigAuditList } from '@/components/admin/config-audit'
import { PageHeader } from '@/components/states'
import { Button } from '@/components/ui/button'

/** Audit of configuration changes; workload events stay on the Activity page. */
export default function AuditPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Administration"
        title="Audit"
        description="Every configuration change with its actor, time, reason and a before/after diff. Nothing is deleted."
        actions={
          <Button asChild size="sm" variant="outline">
            <Link to="/activity">
              <ActivityIcon /> Workload activity
            </Link>
          </Button>
        }
      />
      <ConfigAuditList />
    </div>
  )
}
