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
export type RealizedSavings = S['RealizedSavings']
export type RealizedPoint = S['RealizedPoint']
export type RealizedStep = S['RealizedStep']
export type NodeCostTrend = S['NodeCostTrend']
export type NodeCostPoint = S['NodeCostPoint']
export type Problem = S['Problem']
export type ValueSettings = S['ValueSettings']
export type DataCoverage = S['DataCoverage']
export type WorkloadFacets = S['WorkloadFacets']
export type FacetCount = S['FacetCount']
export type ConfidenceFactors = S['ConfidenceFactors']
export type RecommendationPricing = S['RecommendationPricing']
export type ClusterCapacity = S['ClusterCapacity']
export type CapacityTotals = S['CapacityTotals']
export type CapacityAmounts = S['CapacityAmounts']
export type NodeEstimate = S['NodeEstimate']
export type CapacityPoolEstimate = S['CapacityPoolEstimate']
export type CapacityInstanceType = S['CapacityInstanceType']
export type ConsolidationBlocker = S['ConsolidationBlocker']
export type BlockerType = S['BlockerType']
export type CapacityObjectRef = S['CapacityObjectRef']
export type CapacityAutoscaler = S['CapacityAutoscaler']
export type NodeAutoscalerKind = S['NodeAutoscalerKind']
export type PricingRateSource = S['PricingRateSource']
export type PricingPeriod = S['PricingPeriod']
export type FacetsQuery = NonNullable<operations['getWorkloadFacets']['parameters']['query']>

export type ListWorkloadsQuery = NonNullable<operations['listWorkloads']['parameters']['query']>
export type QueryAuditQuery = NonNullable<operations['queryAudit']['parameters']['query']>
export type UsageQuery = operations['getWorkloadUsage']['parameters']['query']
export type SavingsTrendQuery = NonNullable<operations['getSavingsTrend']['parameters']['query']>

export const WORKLOAD_KINDS: WorkloadKind[] = ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob']
export const MODES: Mode[] = ['off', 'audit', 'auto-patch']
export const PHASES: Phase[] = [
  'Pending',
  'Applying',
  'Applied',
  'Blocked',
  'RolledBack',
  'Released',
  'Deferred',
]
export const RISK_FLAGS: RiskFlag[] = [
  'throttled',
  'oom-killed',
  'restarting',
  'hpa-coupled',
  'sidecar',
  'short-lived',
  'low-confidence',
  'jvm-fixed-heap',
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

// ---- Admin area (WO-19 contract: docs/contracts/admin.md, readiness.md) ----
export type UserRole = S['UserRole']
export type TokenRole = S['TokenRole']
export type Me = S['Me']
export type UserScope = S['UserScope']
export type TeamRole = S['TeamRole']
export type TeamInput = S['TeamInput']
export type Team = S['Team']
export type TeamList = S['TeamList']
export type TeamCounts = S['TeamCounts']
export type TeamSummary = S['TeamSummary']
export type User = S['User']
export type TokenInfo = S['TokenInfo']
export type TokenCreated = S['TokenCreated']
export type Actor = S['Actor']
export type RecommenderSettings = S['RecommenderSettings']
export type RecommenderProfileSettings = S['RecommenderProfileSettings']
export type RecommenderSettingsDoc = S['RecommenderSettingsDoc']
export type PricingSettings = S['PricingSettings']
export type PricingSettingsDoc = S['PricingSettingsDoc']
export type ClusterPricingAdjustment = S['ClusterPricingAdjustment']
export type PricingRates = S['PricingRates']
export type PricingCURStatus = S['PricingCURStatus']
export type Settings = S['Settings']
export type SettingsVersion = S['SettingsVersion']
export type HubPolicyType = S['HubPolicyType']
export type ClusterPolicySource = S['ClusterPolicySource']
export type PolicyOrigin = S['PolicyOrigin']
export type Environment = S['Environment']
export type PolicySelector = S['PolicySelector']
export type NamespacedWorkloadRef = S['NamespacedWorkloadRef']
export type TimeWindow = S['TimeWindow']
export type RightsizingPolicySpec = S['RightsizingPolicySpec']
export type PolicyInput = S['PolicyInput']
export type PolicyDraft = S['PolicyDraft']
export type AdminPolicy = S['AdminPolicy']
export type PolicyVersion = S['PolicyVersion']
export type PolicyInventoryItem = S['PolicyInventoryItem']
export type ClusterPolicyInventory = S['ClusterPolicyInventory']
export type PolicyChange = S['PolicyChange']
export type ImpactPreview = S['ImpactPreview']
export type ImpactItem = S['ImpactItem']
export type Automation = S['Automation']
export type AutomationUpdate = S['AutomationUpdate']
export type FleetAutomation = S['FleetAutomation']
export type AgentConfigSync = S['AgentConfigSync']
export type ClusterAdmin = S['ClusterAdmin']
export type ClusterCreate = S['ClusterCreate']
export type ClusterCreated = S['ClusterCreated']
export type ClusterUpdate = S['ClusterUpdate']
export type Approval = S['Approval']
export type WorkloadOverrideInput = S['WorkloadOverrideInput']
export type LayerSource = S['LayerSource']
export type PolicyLayer = S['PolicyLayer']
export type EffectiveField = S['EffectiveField']
export type EffectivePolicy = S['EffectivePolicy']
export type ConfigTargetType = S['ConfigTargetType']
export type ConfigAction = S['ConfigAction']
export type ConfigChange = S['ConfigChange']
export type DatasourceType = S['DatasourceType']
export type DatasourceAuthType = S['DatasourceAuthType']
export type DatasourceInput = S['DatasourceInput']
export type Datasource = S['Datasource']
export type MetricsBinding = S['MetricsBinding']
export type DatasourceTestResult = S['DatasourceTestResult']
export type ReadinessState = S['ReadinessState']
export type ReadinessSeverity = S['ReadinessSeverity']
export type ReadinessStatus = S['ReadinessStatus']
export type ReadinessCategory = S['ReadinessCategory']
export type ReadinessCheck = S['ReadinessCheck']
export type AgentCapability = S['AgentCapability']
export type ReadinessSummary = S['ReadinessSummary']
export type ClusterReadiness = S['ClusterReadiness']
export type ReadinessHistoryEntry = S['ReadinessHistoryEntry']
export type InstallFormat = S['InstallFormat']
export type InstallSnippet = S['InstallSnippet']
export type Maintenance = S['Maintenance']
export type ComponentVersion = S['ComponentVersion']
export type ComponentName = S['ComponentName']
export type VersionCompat = S['VersionCompat']
export type UpdateState = S['UpdateState']
export type UpdateStatus = S['UpdateStatus']
export type UpgradeCommands = S['UpgradeCommands']
export type NotificationChannelType = S['NotificationChannelType']
export type NotificationEventType = S['NotificationEventType']
export type NotificationDeliveryKind = S['NotificationDeliveryKind']
export type NotificationDeliveryStatus = S['NotificationDeliveryStatus']
export type NotificationChannelInput = S['NotificationChannelInput']
export type NotificationChannel = S['NotificationChannel']
export type NotificationLastDelivery = S['NotificationLastDelivery']
export type NotificationTestResult = S['NotificationTestResult']
export type NotificationRuleInput = S['NotificationRuleInput']
export type NotificationRule = S['NotificationRule']
export type NotificationDigestInput = S['NotificationDigestInput']
export type NotificationDigest = S['NotificationDigest']
export type NotificationDelivery = S['NotificationDelivery']
export type NotificationStatus = S['NotificationStatus']
export type ConfigAuditQuery = NonNullable<operations['queryConfigAudit']['parameters']['query']>

export const USER_ROLES: UserRole[] = ['viewer', 'operator', 'admin']
export const READINESS_STATES: ReadinessState[] = [
  'Registered',
  'AgentConnected',
  'MetricsOK',
  'ReadyAudit',
  'AutomationReady',
]
export type PasswordResetResult = S['PasswordResetResult']
