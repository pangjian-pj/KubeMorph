import { http } from './http'

export type Provider = 'AWS' | 'ACK' | 'TKE' | 'VKE' | 'GKE' | 'Azure' | 'Onprem'

export interface ImportClusterRequest {
  name: string
  description?: string
  provider?: Provider
  kubeconfig: string
  labels?: Record<string, string>
}

export interface ImportClusterResponse {
  clusterId?: string
  name?: string
  status?: string
  message?: string
}

export interface ClusterSummary {
  clusterId?: string
  name?: string
  description?: string
  provider?: Provider | string
  status?: string
  apiEndpoint?: string
  labels?: Record<string, string>
  createdAt?: string
  updatedAt?: string
  message?: string
}

export interface ListClustersResponse {
  total: number
  page: number
  pageSize: number
  items: ClusterSummary[]
}

export async function importCluster(payload: ImportClusterRequest): Promise<ImportClusterResponse> {
  // baseURL 默认是 /api，因此这里是 POST /api/v1/ImportCluster
  const { data } = await http.post<ImportClusterResponse>('/v1/ImportCluster', payload)
  return data
}

export async function listClusters(params?: {
  page?: number
  pageSize?: number
  name?: string
  provider?: string
  status?: string
  labelSelector?: string
}): Promise<ListClustersResponse> {
  const { data } = await http.get<ListClustersResponse>('/v1/ListClusters', { params })
  return data
}

export interface NodeItem {
  name?: string
  uid?: string
  ready?: boolean
  labels?: Record<string, string>

  allocatable?: { cpu?: string; memory?: string }
  requested?: { cpu?: string; memory?: string }
  free?: { cpu?: string; memory?: string }
}

export interface GetClusterResponse {
  cluster: ClusterSummary
  kubeconfig?: string
  nodes?: NodeItem[]
  resources?: {
    capacity?: { cpu?: string; memory?: string }
    allocatable?: { cpu?: string; memory?: string }
    nodeCount?: number
    podCount?: number
  }
  secretName?: string
  apiEndpoint?: string
}

export async function getCluster(params: { clusterId: string }): Promise<GetClusterResponse> {
  const { data } = await http.get<GetClusterResponse>('/v1/GetCluster', { params })
  return data
}

export interface DeleteClustersRequest {
  clusterIds: string[]
}

export interface DeleteClustersResponse {
  deleted: string[]
  failed: Array<{ clusterId: string; message: string }>
  message?: string
}

export async function deleteClusters(payload: DeleteClustersRequest): Promise<DeleteClustersResponse> {
  const { data } = await http.post<DeleteClustersResponse>('/v1/DeleteClusters', payload)
  return data
}

export interface UpdateClusterRequest {
  clusterId: string
  name?: string
  description?: string
  provider?: Provider | string
  labels?: Record<string, string>
}

export interface UpdateClusterResponse {
  clusterId?: string
  name?: string
  description?: string
  provider?: Provider | string
  labels?: Record<string, string>
  updatedAt?: string
  message?: string
}

export async function updateCluster(payload: UpdateClusterRequest): Promise<UpdateClusterResponse> {
  const { data } = await http.post<UpdateClusterResponse>('/v1/UpdateCluster', payload)
  return data
}
