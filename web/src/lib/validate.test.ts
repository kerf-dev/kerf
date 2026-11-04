import { describe, expect, it } from 'vitest'
import {
  globMatch,
  validateCron,
  validateGlob,
  validateName,
  validatePriority,
  validateSpec,
} from './validate'

describe('validateSpec mirrors api/v1alpha1/validate', () => {
  it('accepts a full valid spec', () => {
    expect(
      validateSpec({
        cpu: { mode: 'auto-patch', limits: 'ratio', limitRatio: '1.5' },
        memory: { mode: 'audit', limitEqualsRequest: true },
        guardrails: {
          learningPeriod: '168h',
          minConfidence: '0.8',
          maxChangePercent: 50,
          minCPU: '10m',
          minMemory: '32Mi',
          cooldown: '24h',
          rollback: { watchDuration: '30m', oomKills: 1, restarts: 3, throttlingRatio: '0.25' },
        },
        hpa: { coordinate: true, targetBounds: { minUtilization: 50, maxUtilization: 80 } },
        apply: {
          strategy: 'in-place',
          maintenanceWindows: [{ schedule: '0 2 * * 1-5', duration: '2h', timeZone: 'Europe/Berlin' }],
          freezeWindows: [{ start: '2026-12-20T00:00:00Z', end: '2027-01-02T00:00:00Z' }],
        },
        selector: {
          namespaces: ['payments', 'team-*'],
          matchLabels: { 'app.kubernetes.io/part-of': 'shop' },
        },
      }),
    ).toEqual({})
  })
  it.each([
    [{ cpu: { limits: 'ratio' as const } }, 'cpu.limitRatio'],
    [{ cpu: { limits: 'keep' as const, limitRatio: '2' } }, 'cpu.limitRatio'],
    [{ cpu: { limits: 'ratio' as const, limitRatio: '0.5' } }, 'cpu.limitRatio'],
    [{ guardrails: { minConfidence: '1.5' } }, 'guardrails.minConfidence'],
    [{ guardrails: { maxChangePercent: 0 } }, 'guardrails.maxChangePercent'],
    [{ guardrails: { cooldown: '1 day' } }, 'guardrails.cooldown'],
    [{ guardrails: { minMemory: '1GB' } }, 'guardrails.minMemory'],
    [
      { hpa: { targetBounds: { minUtilization: 90, maxUtilization: 60 } } },
      'hpa.targetBounds.maxUtilization',
    ],
    [
      { apply: { maintenanceWindows: [{ schedule: '0 2 * *', duration: '1h' }] } },
      'apply.maintenanceWindows.0.schedule',
    ],
    [{ apply: { maintenanceWindows: [{ schedule: '0 2 * * *' }] } }, 'apply.maintenanceWindows.0.duration'],
    [
      { apply: { freezeWindows: [{ start: '2026-12-20T00:00:00Z', end: '2026-12-19T00:00:00Z' }] } },
      'apply.freezeWindows.0.end',
    ],
    [
      { apply: { freezeWindows: [{ schedule: '0 0 * * *', duration: '1h', timeZone: 'Mars/Base' }] } },
      'apply.freezeWindows.0.timeZone',
    ],
    [{ selector: { namespaces: ['Payments'] } }, 'selector.namespaces'],
    [{ selector: { matchLabels: { 'bad key!': 'x' } } }, 'selector.matchLabels'],
  ])('rejects %j at %s', (spec, path) => {
    expect(Object.keys(validateSpec(spec))).toContain(path)
  })
  it('rejects a selector on defaults', () => {
    expect(validateSpec({ selector: { namespaces: ['a'] } }, 'global-default')).toHaveProperty('selector')
  })
})

describe('names, priorities, cron, globs', () => {
  it('reserves ro- and checks the band', () => {
    expect(validateName('ro-mine', 'policy')).toMatch(/reserved/)
    expect(validateName('Payments', 'policy')).toBeTruthy()
    expect(validateName('payments-cpu-auto', 'policy')).toBeUndefined()
    expect(validatePriority(0, 'policy')).toBeTruthy()
    expect(validatePriority(900000, 'policy')).toBeTruthy()
    expect(validatePriority(100, 'policy')).toBeUndefined()
  })
  it('checks cron fields', () => {
    expect(validateCron('*/15 2-4 * * MON-FRI')).toBeUndefined()
    expect(validateCron('61 * * * *')).toMatch(/out of range/)
  })
  it('matches globs like path.Match', () => {
    expect(globMatch('team-*', 'team-a')).toBe(true)
    expect(globMatch('team-?', 'team-ab')).toBe(false)
    expect(globMatch('kube-[sp]*', 'kube-system')).toBe(true)
    expect(validateGlob('team-[a')).toMatch(/brackets/)
  })
})
