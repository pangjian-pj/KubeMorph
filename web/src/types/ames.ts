export interface Cluster {
  name: string
  provider?: string
  status?: string
}

export interface FederatedObject {
  name: string
  namespace: string
  replicas: number
}

export interface OptimizationPolicy {
  name: string
  status?: string
  lastRunAt?: string
}
