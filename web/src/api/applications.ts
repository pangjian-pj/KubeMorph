import { http } from './http'

export type CreateGlobalDeploymentRequest = {
  // Raw YAML text. The server will parse it into GlobalDeployment or Deployment.
  yaml: string
}

export type CreateGlobalDeploymentResponse = {
  name?: string
  namespace?: string
  replicas?: number
  message?: string
}

export async function createGlobalDeployment(req: CreateGlobalDeploymentRequest) {
  // baseURL 默认是 /api，因此这里是 POST /api/v1/CreateGlobalDeployment
  const { data } = await http.post<CreateGlobalDeploymentResponse>('/v1/CreateGlobalDeployment', req)
  return data
}

export type ListGlobalDeploymentsRequest = {
  namespace?: string
  name?: string
  page?: number
  pageSize?: number
}

export type GlobalDeploymentItem = {
  name: string
  namespace: string
  replicas: number
  runningReplicas: number
  failedReplicas: number
  phase: string
  creationTimestamp: string
}

export type ListGlobalDeploymentsResponse = {
  total: number
  items: GlobalDeploymentItem[]
}

export async function listGlobalDeployments(req: ListGlobalDeploymentsRequest) {
  const { data } = await http.post<ListGlobalDeploymentsResponse>('/v1/ListGlobalDeployments', req)
  return data
}

export type GetGlobalDeploymentRequest = {
  name: string
  namespace: string
}

export type GetGlobalDeploymentBinding = {
  replicaIndex: number
  clusterId: string
  clusterName?: string
  nodeName: string
  phase: string
  lastError?: string
  lastTransitionTime?: string
  podName?: string
  podIP?: string
  attempt?: number
}

export type GetGlobalDeploymentResponse = {
  name: string
  namespace: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  replicas: number
  runningReplicas: number
  failedReplicas: number
  phase: string
  template?: any
  bindings: GetGlobalDeploymentBinding[]
}

export async function getGlobalDeployment(req: GetGlobalDeploymentRequest) {
  const { data } = await http.post<GetGlobalDeploymentResponse>('/v1/GetGlobalDeployment', req)
  return data
}

export type DeleteGlobalDeploymentRequest = {
  name: string
  namespace: string
}

export type DeleteGlobalDeploymentResponse = {
  message?: string
}

export async function deleteGlobalDeployment(req: DeleteGlobalDeploymentRequest) {
  const { data } = await http.post<DeleteGlobalDeploymentResponse>('/v1/DeleteGlobalDeployment', req)
  return data
}
