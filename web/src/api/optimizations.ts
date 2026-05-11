import { http } from './http'

// ---- policies ----

export type ListOptimizationPoliciesRequest = {
  namespace?: string
  name?: string
  page?: number
  pageSize?: number
}

export type OptimizationPolicyItem = {
  name: string
  namespace: string
  enabled: boolean
  runMode?: string
  rebalancePoint?: string
  strategy?: string
  thresholdPercent?: number
  phase?: string
  lastEvaluationTime?: string
  creationTimestamp?: string
}

export type ListOptimizationPoliciesResponse = {
  total: number
  items: OptimizationPolicyItem[]
}

export async function listOptimizationPolicies(req: ListOptimizationPoliciesRequest) {
  const { data } = await http.post<ListOptimizationPoliciesResponse>('/v1/ListOptimizationPolicies', req)
  return data
}

export type GetOptimizationPolicyRequest = {
  namespace?: string
  name: string
}

export type GetOptimizationPolicyResponse = {
  policy: any
}

export async function getOptimizationPolicy(req: GetOptimizationPolicyRequest) {
  const { data } = await http.post<GetOptimizationPolicyResponse>('/v1/GetOptimizationPolicy', req)
  return data
}

export type ApplyOptimizationPolicyRequest = {
  policy: any
}

export type ApplyOptimizationPolicyResponse = {
  message?: string
}

export async function createOptimizationPolicy(req: ApplyOptimizationPolicyRequest) {
  const { data } = await http.post<ApplyOptimizationPolicyResponse>('/v1/CreateOptimizationPolicy', req)
  return data
}

export async function updateOptimizationPolicy(req: ApplyOptimizationPolicyRequest) {
  const { data } = await http.post<ApplyOptimizationPolicyResponse>('/v1/UpdateOptimizationPolicy', req)
  return data
}

export type DeleteOptimizationPolicyRequest = {
  namespace?: string
  name: string
}

export type DeleteOptimizationPolicyResponse = {
  message?: string
}

export async function deleteOptimizationPolicy(req: DeleteOptimizationPolicyRequest) {
  const { data } = await http.post<DeleteOptimizationPolicyResponse>('/v1/DeleteOptimizationPolicy', req)
  return data
}

// ---- plans ----

export type ListReOrchestrationPlansRequest = {
  namespace?: string
  policyName?: string
  page?: number
  pageSize?: number
}

export type ReOrchestrationPlanItem = {
  name: string
  namespace: string
  policyName?: string
  phase?: string
  moves: number
  creationTimestamp?: string
}

export type ListReOrchestrationPlansResponse = {
  total: number
  items: ReOrchestrationPlanItem[]
}

export async function listReOrchestrationPlans(req: ListReOrchestrationPlansRequest) {
  const { data } = await http.post<ListReOrchestrationPlansResponse>('/v1/ListReOrchestrationPlans', req)
  return data
}

export type GetReOrchestrationPlanRequest = {
  namespace?: string
  name: string
}

export type GetReOrchestrationPlanResponse = {
  plan: any
}

export async function getReOrchestrationPlan(req: GetReOrchestrationPlanRequest) {
  const { data } = await http.post<GetReOrchestrationPlanResponse>('/v1/GetReOrchestrationPlan', req)
  return data
}

// ---- topology ----

export type ListTopologyConfigsRequest = {
  namespace?: string
}

export type TopologyConfigItem = {
  name: string
  namespace: string
  creationTimestamp?: string
}

export type ListTopologyConfigsResponse = {
  items: TopologyConfigItem[]
}

export async function listTopologyConfigs(req: ListTopologyConfigsRequest = {}) {
  const { data } = await http.post<ListTopologyConfigsResponse>('/v1/ListTopologyConfigs', req)
  return data
}
