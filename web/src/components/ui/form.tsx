import { CheckIcon, ChevronDownIcon } from 'lucide-react'
import { Checkbox as CheckboxPrimitive, Switch as SwitchPrimitive } from 'radix-ui'
import * as React from 'react'
import { cn } from '@/lib/utils'

export function Label({ className, ...props }: React.ComponentProps<'label'>) {
  return <label className={cn('text-[13px] leading-none font-medium select-none', className)} {...props} />
}

export function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      className={cn(
        'flex min-h-16 w-full rounded-md border border-input bg-card px-2.5 py-2 text-sm shadow-xs transition-[color,box-shadow] outline-none placeholder:text-muted-foreground/80 focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/30 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20',
        className,
      )}
      {...props}
    />
  )
}

/** A styled native select: accessible, keyboard- and mobile-friendly, no portal. */
export function NativeSelect({ className, children, ...props }: React.ComponentProps<'select'>) {
  return (
    <div className={cn('relative', className)}>
      <select
        className="h-8 w-full min-w-0 appearance-none rounded-md border border-input bg-card py-1 pr-8 pl-2.5 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/30 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive"
        {...props}
      >
        {children}
      </select>
      <ChevronDownIcon className="pointer-events-none absolute top-1/2 right-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
    </div>
  )
}

export function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        'peer inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent shadow-xs transition-all outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-input',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="pointer-events-none block size-4 rounded-full bg-white shadow-sm ring-0 transition-transform data-[state=checked]:translate-x-[calc(100%-0px)] data-[state=unchecked]:translate-x-0.5" />
    </SwitchPrimitive.Root>
  )
}

export function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      className={cn(
        'peer size-4 shrink-0 rounded-[4px] border border-input bg-card shadow-xs transition-shadow outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground data-[state=indeterminate]:border-primary data-[state=indeterminate]:bg-primary data-[state=indeterminate]:text-primary-foreground',
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator className="flex items-center justify-center text-current">
        {props.checked === 'indeterminate' ? (
          <span className="h-0.5 w-2 rounded bg-current" />
        ) : (
          <CheckIcon className="size-3" strokeWidth={3} />
        )}
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  )
}

/**
 * A labelled form row. `hint` explains the field; `inherited` shows what applies when the field is
 * left empty (the value from the layer below, or the built-in default); `error` comes from validation.
 */
export function Field({
  label,
  htmlFor,
  hint,
  inherited,
  error,
  children,
  className,
  optional,
}: {
  label: React.ReactNode
  htmlFor?: string
  hint?: React.ReactNode
  inherited?: React.ReactNode
  error?: string
  children: React.ReactNode
  className?: string
  optional?: boolean
}) {
  return (
    <div className={cn('flex min-w-0 flex-col gap-1.5', className)}>
      <Label htmlFor={htmlFor} className="leading-snug">
        {label}
        {optional ? <span className="ml-1 font-normal text-muted-foreground">(optional)</span> : null}
      </Label>
      {children}
      {error ? (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      ) : hint || inherited != null ? (
        <p className="text-xs text-pretty text-muted-foreground">
          {inherited != null ? <span className="text-foreground/70">{inherited}</span> : null}
          {inherited != null && hint ? ' · ' : null}
          {hint}
        </p>
      ) : null}
    </div>
  )
}

/** A titled group of fields inside a form card. */
export function FormSection({
  title,
  description,
  children,
  className,
  aside,
}: {
  title: React.ReactNode
  description?: React.ReactNode
  children: React.ReactNode
  className?: string
  aside?: React.ReactNode
}) {
  return (
    <section
      className={cn(
        'grid gap-x-8 gap-y-4 py-6 first:pt-0 last:pb-0 lg:grid-cols-[minmax(0,17rem)_1fr]',
        className,
      )}
    >
      <div>
        <h3 className="text-sm font-semibold">{title}</h3>
        {description ? (
          <p className="mt-1 text-[13px] text-pretty text-muted-foreground">{description}</p>
        ) : null}
        {aside}
      </div>
      <div className="@container min-w-0">{children}</div>
    </section>
  )
}

/** Inline callout for notices inside pages and dialogs. */
export function Callout({
  tone = 'info',
  icon: Icon,
  title,
  children,
  className,
  action,
}: {
  tone?: 'info' | 'warning' | 'destructive' | 'success' | 'muted'
  icon?: React.ComponentType<{ className?: string }>
  title?: React.ReactNode
  children?: React.ReactNode
  className?: string
  action?: React.ReactNode
}) {
  const tones = {
    info: 'border-info/25 bg-info/6 [&_.co-icon]:text-info',
    warning:
      'border-warning/35 bg-warning/8 [&_.co-icon]:text-[color-mix(in_oklch,var(--warning)_75%,var(--foreground))]',
    destructive: 'border-destructive/25 bg-destructive/6 [&_.co-icon]:text-destructive',
    success: 'border-success/25 bg-success/6 [&_.co-icon]:text-success',
    muted: 'border-border bg-muted/50 [&_.co-icon]:text-muted-foreground',
  }
  return (
    <div
      className={cn(
        'flex items-start gap-3 rounded-lg border px-3.5 py-3 text-[13px]',
        tones[tone],
        className,
      )}
    >
      {Icon ? <Icon className="co-icon mt-0.5 size-4 shrink-0" /> : null}
      <div className="min-w-0 flex-1">
        {title ? <div className="font-medium">{title}</div> : null}
        {children ? (
          <div className={cn('text-pretty text-muted-foreground', title && 'mt-0.5')}>{children}</div>
        ) : null}
      </div>
      {action ? <div className="shrink-0">{action}</div> : null}
    </div>
  )
}
