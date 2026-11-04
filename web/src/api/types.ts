import type { components, operations } from './schema'

type S = components['schemas']

export type ClusterName = S['ClusterName']
export type WorkloadKind = S['WorkloadKind']
export type Mode = S['Mode']
export type ModeSource = S['ModeSource']
export type Strategy = S['Strategy']
export type Profile = S['Profile']
export type Phase = S['Phase']
export type RiskFlag = S['RiskFlag']
export type EventType = S['EventType']
export type ResourceName = S['ResourceName']
export type Resources = S['Resources']
export type ResourceValues = S['ResourceValues']
export type ContainerRecommendation = S['ContainerRecommendation']
export type ContainerExplanation = S['ContainerExplanation']
export type ResourceExplanation = S['ResourceExplanation']
export type Savings = S['Savings']
export type WorkloadRecommendation = S['WorkloadRecommendation']
export type WorkloadStatus = S['WorkloadStatus']
export type EffectiveModes = S['EffectiveModes']
export type AuditEvent = S['AuditEvent']
export type AuditEventList = S['AuditEventList']
export type Summary = S['Summary']
export type ClusterSummary = S['ClusterSummary']
export type FleetSummary = S['FleetSummary']
export type ClusterInfo = S['ClusterInfo']
export type ClusterList = S['ClusterList']
export type WorkloadRow = S['WorkloadRow']
export type WorkloadList = S['WorkloadList']
export type WorkloadDetail = S['WorkloadDetail']
export type UsageSeries = S['UsageSeries']
export type ContainerSeries = S['ContainerSeries']
export type Point = S['Point']
export type WorkloadHistory = S['WorkloadHistory']
export type SavingsTrend = S['SavingsTrend']
export type SavingsPoint = S['SavingsPoint']
export type Problem = S['Problem']
export type ValueSettings = S['ValueSettings']
export type DataCoverage = S['DataCoverage']

export type ListWorkloadsQuery = NonNullable<operations['listWorkloads']['parameters']['query']>
export type QueryAuditQuery = NonNullable<operations['queryAudit']['parameters']['query']>
export type UsageQuery = operations['getWorkloadUsage']['parameters']['query']
export type SavingsTrendQuery = NonNullable<operations['getSavingsTrend']['parameters']['query']>

export const WORKLOAD_KINDS: WorkloadKind[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob']
export const MODES: Mode[] = ['off', 'audit', 'auto-patch']
export const PHASES: Phase[] = ['Pending', 'Applying', 'Applied', 'Blocked', 'RolledBack', 'Released']
export const RISK_FLAGS: RiskFlag[] = [
  'throttled',
  'oom-killed',
  'restarting',
  'hpa-coupled',
  'sidecar',
  'short-lived',
  'low-confidence',
]
export const EVENT_TYPES: EventType[] = [
  'applied',
  'skipped',
  'blocked',
  'fallback',
  'rolled_back',
  'released',
  'failed',
]
