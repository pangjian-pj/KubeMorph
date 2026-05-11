import { createRouter, createWebHistory } from 'vue-router'

import ClustersView from '@/views/ClustersView.vue'
import ClusterDetailView from '@/views/ClusterDetailView.vue'
import ApplicationsView from '@/views/ApplicationsView.vue'
import ApplicationDetailView from '@/views/ApplicationDetailView.vue'
import OptimizationsView from '@/views/OptimizationsView.vue'
import OptimizationPolicyCreateView from '@/views/OptimizationPolicyCreateView.vue'
import OptimizationPolicyDetailView from '@/views/OptimizationPolicyDetailView.vue'
import ReOrchestrationPlanDetailView from '@/views/ReOrchestrationPlanDetailView.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/clusters' },
    { path: '/clusters', component: ClustersView },
    { path: '/clusters/:clusterId', component: ClusterDetailView },
    { path: '/applications', component: ApplicationsView },
    { path: '/applications/:namespace/:name', component: ApplicationDetailView },
    { path: '/optimizations', component: OptimizationsView },
  { path: '/optimizations/policies/create', component: OptimizationPolicyCreateView },
    { path: '/optimizations/policies/:namespace/:name', component: OptimizationPolicyDetailView },
    { path: '/optimizations/plans/:namespace/:name', component: ReOrchestrationPlanDetailView },
  ],
})

export default router
