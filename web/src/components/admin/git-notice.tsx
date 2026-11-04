import { GitBranchIcon } from 'lucide-react'
import { Callout } from '@/components/ui/form'

/** Explains why a policy edit does not apply to Git-only clusters (`policySource: git`, D-022). */
export function GitOnlyNotice({ clusters, what = 'This change' }: { clusters: string[]; what?: string }) {
  if (!clusters.length) return null
  return (
    <Callout
      tone="muted"
      icon={GitBranchIcon}
      title={`Not sent to ${clusters.length === 1 ? 'the Git-only cluster' : 'Git-only clusters'} ${clusters.join(', ')}`}
    >
      {what} does not reach {clusters.length === 1 ? 'it' : 'them'}: {clusters.length === 1 ? 'its' : 'their'}{' '}
      policies are managed in Git (policy source “Git only”). Use Export YAML and commit the policy there.
    </Callout>
  )
}
