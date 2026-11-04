import { KeyRoundIcon, Loader2Icon } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { ApiError } from '@/api/client'
import { useChangePassword } from '@/api/auth'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Callout, Field } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { toast } from '@/components/ui/toast'

/** Password rules from hub.yaml `Password`: 8 characters to 72 bytes (bcrypt). */
export function passwordProblem(pw: string): string | undefined {
  if (pw.length < 8) return 'At least 8 characters.'
  if (new TextEncoder().encode(pw).length > 72) return 'At most 72 bytes.'
  return undefined
}

/**
 * Change your own password. `forced` after an admin reset (`mustChangePassword`): the dialog cannot be
 * dismissed until the password is changed.
 */
export function ChangePasswordDialog({
  open,
  onOpenChange,
  forced,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  forced?: boolean
}) {
  const change = useChangePassword()
  const id = useId()
  const [cur, setCur] = useState('')
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (open) {
      setCur('')
      setPw('')
      setPw2('')
      setTouched(false)
      change.reset()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])
  const pwErr = passwordProblem(pw)
  const matchErr =
    pw2 && pw !== pw2 ? 'The passwords do not match.' : !pw2 ? 'Repeat the new password.' : undefined
  const err = change.error instanceof ApiError ? change.error : undefined
  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!cur || pwErr || matchErr) return
    change.mutate(
      { currentPassword: cur, newPassword: pw },
      {
        onSuccess: () => {
          toast.success('Password changed')
          onOpenChange(false)
        },
      },
    )
  }
  return (
    <Dialog open={open} onOpenChange={(o) => (!forced || !o ? onOpenChange(o) : undefined)}>
      <DialogContent
        className="top-[12%] w-[min(440px,calc(100vw-2rem))]"
        onEscapeKeyDown={(e) => forced && e.preventDefault()}
        onPointerDownOutside={(e) => forced && e.preventDefault()}
        data-testid="change-password"
      >
        <form onSubmit={submit} className="flex flex-col gap-4 p-5" noValidate>
          <div className="flex items-start gap-3">
            <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/12">
              <KeyRoundIcon className="size-4 text-primary" />
            </div>
            <div>
              <DialogTitle className="text-base font-semibold">
                {forced ? 'Choose a new password' : 'Change password'}
              </DialogTitle>
              <DialogDescription className="mt-0.5 text-[13px] text-muted-foreground">
                {forced
                  ? 'An admin reset your password. Set your own before you continue.'
                  : 'Other sessions stay signed in until they expire.'}
              </DialogDescription>
            </div>
          </div>
          {err ? (
            <Callout tone="destructive">
              {err.status === 401 || err.status === 403 || err.code === 'invalid_credentials'
                ? 'The current password is wrong.'
                : err.message}
            </Callout>
          ) : null}
          <Field
            label={forced ? 'Temporary password' : 'Current password'}
            htmlFor={`${id}-c`}
            error={touched && !cur ? 'Required.' : undefined}
          >
            <Input
              id={`${id}-c`}
              type="password"
              autoComplete="current-password"
              value={cur}
              onChange={(e) => setCur(e.target.value)}
              autoFocus
            />
          </Field>
          <Field
            label="New password"
            htmlFor={`${id}-n`}
            hint="At least 8 characters."
            error={touched ? pwErr : undefined}
          >
            <Input
              id={`${id}-n`}
              type="password"
              autoComplete="new-password"
              value={pw}
              onChange={(e) => setPw(e.target.value)}
            />
          </Field>
          <Field label="Repeat new password" htmlFor={`${id}-r`} error={touched ? matchErr : undefined}>
            <Input
              id={`${id}-r`}
              type="password"
              autoComplete="new-password"
              value={pw2}
              onChange={(e) => setPw2(e.target.value)}
            />
          </Field>
          <div className="flex justify-end gap-2 pt-1">
            {!forced ? (
              <Button type="button" variant="ghost" size="sm" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
            ) : null}
            <Button type="submit" size="sm" disabled={change.isPending}>
              {change.isPending ? <Loader2Icon className="animate-spin" /> : null}
              Change password
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
