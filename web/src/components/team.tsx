import { UsersRoundIcon } from 'lucide-react'
import { Link } from 'react-router'
import { useTeamLabel } from '@/api/admin'
import { Badge } from '@/components/ui/badge'
import { UNASSIGNED } from '@/lib/teams'

/** A team as a chip that opens the Opportunities list filtered to it. */
export function TeamChip({ team, label, className }: { team: string; label?: string; className?: string }) {
  const name = useTeamLabel()
  return (
    <Link
      to={`/opportunities?team=${encodeURIComponent(team)}`}
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => e.stopPropagation()}
      className="rounded-md outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40"
      data-testid="team-chip"
      aria-label={`Workloads of team ${label ?? name(team)}`}
    >
      <Badge variant={team === UNASSIGNED ? 'muted' : 'secondary'} className={className}>
        <UsersRoundIcon />
        {label ?? name(team)}
      </Badge>
    </Link>
  )
}
