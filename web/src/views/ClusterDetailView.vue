<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { getCluster, updateCluster, type GetClusterResponse, type NodeItem, type Provider } from '@/api/clusters'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const clusterId = computed(() => String(route.params.clusterId || ''))

const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<GetClusterResponse | null>(null)

const activeTab = ref<'kubeconfig' | 'resources' | 'nodes'>('kubeconfig')

const showUpdate = ref(false)
const updating = ref(false)
const updateError = ref<string | null>(null)

const updateForm = ref({
  name: '',
  description: '',
  provider: 'Onprem' as Provider,
  labelsText: '',
})

function labelEntries(labels?: Record<string, string>) {
  return Object.entries(labels || {}).sort((a, b) => a[0].localeCompare(b[0]))
}

function buildLabelsText(labels?: Record<string, string>) {
  const entries = labelEntries(labels)
  if (!entries.length) return ''
  return entries.map(([k, v]) => `${k}=${v}`).join('\n')
}

function parseLabels(text: string): Record<string, string> | undefined {
  const t = (text || '').trim()
  if (!t) return undefined
  if (t.startsWith('{')) {
    const obj = JSON.parse(t) as Record<string, unknown>
    const labels: Record<string, string> = {}
    for (const [k, v] of Object.entries(obj)) {
      if (typeof v === 'string') labels[k] = v
    }
    return labels
  }
  const labels: Record<string, string> = {}
  for (const line of t.split(/\r?\n/)) {
    const s = line.trim()
    if (!s) continue
    const idx = s.indexOf('=')
    if (idx <= 0) continue
    const k = s.slice(0, idx).trim()
    const v = s.slice(idx + 1).trim()
    if (k && v) labels[k] = v
  }
  return Object.keys(labels).length ? labels : undefined
}

async function refresh() {
  if (!clusterId.value) return
  loading.value = true
  error.value = null
  try {
    data.value = await getCluster({ clusterId: clusterId.value })
  } catch (e: any) {
    error.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    loading.value = false
  }
}

function openUpdate() {
  const c = data.value?.cluster
  if (!c) return
  updateError.value = null
  updateForm.value = {
    name: c.name || '',
    description: c.description || '',
  provider: ((c.provider as Provider) || 'Onprem') as Provider,
    labelsText: buildLabelsText(c.labels),
  }
  showUpdate.value = true
}

async function onSubmitUpdate() {
  if (!clusterId.value) return
  updating.value = true
  updateError.value = null
  try {
    await updateCluster({
      clusterId: clusterId.value,
      name: updateForm.value.name.trim() || undefined,
      description: updateForm.value.description, // 允许清空
      provider: updateForm.value.provider || undefined,
      labels: parseLabels(updateForm.value.labelsText),
    })
    showUpdate.value = false
    await refresh()
  } catch (e: any) {
    updateError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    updating.value = false
  }
}

function goBack() {
  router.push('/clusters')
}

function fmt(v?: string) {
  if (!v) return '-'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  return d.toLocaleString()
}

function parseCpuToCores(cpu?: string): number | null {
  if (!cpu) return null
  const s = cpu.trim()
  if (!s) return null

  // k8s quantity 常见："250m" 或 "2" 或 "1.5"
  if (s.endsWith('m')) {
    const n = Number(s.slice(0, -1))
    if (Number.isFinite(n)) return n / 1000
    return null
  }
  const n = Number(s)
  return Number.isFinite(n) ? n : null
}

function formatCpuCores(cpu?: string) {
  const cores = parseCpuToCores(cpu)
  if (cores == null) return '-'
  // 展示为“核”，尽量别太长
  return `${Number(cores.toFixed(3))} Core`
}

function parseBytesFromQuantity(mem?: string): number | null {
  if (!mem) return null
  const s = mem.trim()
  if (!s) return null

  // 仅覆盖我们预计会遇到的：Ki/Mi/Gi/Ti/Pi/Ei 以及 K/M/G/T/P/E（按 10^3）
  const m = s.match(/^([0-9]+(?:\.[0-9]+)?)([a-zA-Z]+)?$/)
  if (!m) return null
  const value = Number(m[1])
  if (!Number.isFinite(value)) return null
  const unit = (m[2] || '').toLowerCase()

  const pow2: Record<string, number> = {
    ki: 1024,
    mi: 1024 ** 2,
    gi: 1024 ** 3,
    ti: 1024 ** 4,
    pi: 1024 ** 5,
    ei: 1024 ** 6,
  }
  const pow10: Record<string, number> = {
    k: 1000,
    m: 1000 ** 2,
    g: 1000 ** 3,
    t: 1000 ** 4,
    p: 1000 ** 5,
    e: 1000 ** 6,
  }

  if (!unit) return value
  if (pow2[unit] != null) return value * pow2[unit]
  if (pow10[unit] != null) return value * pow10[unit]
  return null
}

function formatMemGi(mem?: string) {
  const bytes = parseBytesFromQuantity(mem)
  if (bytes == null) return '-'
  const gi = bytes / 1024 / 1024 / 1024
  return `${Number(gi.toFixed(2))} Gi`
}

function nodeKey(n: NodeItem, idx: number) {
  return n.name || String(idx)
}

const cluster = computed(() => data.value?.cluster)
const resources = computed(() => data.value?.resources)

function getHostname(labels?: Record<string, string>) {
  return labels?.['kubernetes.io/hostname'] || '-'
}

onMounted(() => {
  void refresh()
})
</script>

<template>
  <section class="page">
    <div class="topbar">
      <div>
        <h1>{{ t('clusters.detail.title') }}</h1>
        <p class="muted">{{ clusterId }}</p>
      </div>

      <div class="actions">
  <button class="btn" @click="openUpdate" :disabled="loading || !cluster">{{ t('clusters.update.button') }}</button>
        <button class="btn" @click="refresh" :disabled="loading">{{ t('clusters.detail.refresh') }}</button>
        <button class="btn" @click="goBack">{{ t('clusters.detail.back') }}</button>
      </div>
    </div>

    <div class="card" v-if="loading">
      <div class="pad muted">{{ t('clusters.detail.loading') }}</div>
    </div>

    <div class="card" v-else-if="error">
      <div class="pad error">{{ error }}</div>
    </div>

    <template v-else-if="cluster">
      <div class="card">
        <div class="pad">
          <div class="grid">
            <div class="kv">
              <div class="k">{{ t('clusters.detail.clusterId') }}</div>
              <div class="v mono">{{ cluster.clusterId || '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.name') }}</div>
              <div class="v">{{ cluster.name || '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.description') }}</div>
              <div class="v">{{ cluster.description || '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.provider') }}</div>
              <div class="v">{{ cluster.provider || '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.status') }}</div>
              <div class="v">
                <span class="pill" :class="String(cluster.status || '').toLowerCase()">{{ cluster.status || '-' }}</span>
              </div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.apiEndpoint') }}</div>
              <div class="v mono">{{ cluster.apiEndpoint || data?.apiEndpoint || '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.secretName') }}</div>
              <div class="v mono">{{ data?.secretName || '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.createdAt') }}</div>
              <div class="v">{{ fmt(cluster.createdAt) }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.updatedAt') }}</div>
              <div class="v">{{ fmt(cluster.updatedAt) }}</div>
            </div>
          </div>

          <div class="labels" v-if="cluster.labels && Object.keys(cluster.labels).length">
            <span v-for="(v, k) in cluster.labels" :key="k" class="tag">{{ k }}={{ v }}</span>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="tabs">
          <button
            class="tab"
            :class="{ active: activeTab === 'kubeconfig' }"
            @click="activeTab = 'kubeconfig'"
          >
            {{ t('clusters.detail.tabs.kubeconfig') }}
          </button>
          <button class="tab" :class="{ active: activeTab === 'resources' }" @click="activeTab = 'resources'">
            {{ t('clusters.detail.tabs.resources') }}
          </button>
          <button class="tab" :class="{ active: activeTab === 'nodes' }" @click="activeTab = 'nodes'">
            {{ t('clusters.detail.tabs.nodes') }}
            <span v-if="data?.nodes" class="badge">{{ data.nodes.length }}</span>
          </button>
        </div>

        <div class="pad" v-if="activeTab === 'kubeconfig'">
          <pre class="code">{{ data?.kubeconfig || '-' }}</pre>
        </div>

        <div class="pad" v-else-if="activeTab === 'resources'">
          <div class="grid">
            <div class="kv">
              <div class="k">{{ t('clusters.detail.resources.capacityCpu') }}</div>
              <div class="v">{{ formatCpuCores(resources?.capacity?.cpu) }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.resources.capacityMem') }}</div>
              <div class="v">{{ formatMemGi(resources?.capacity?.memory) }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.resources.allocatableCpu') }}</div>
              <div class="v">{{ formatCpuCores(resources?.allocatable?.cpu) }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.resources.allocatableMem') }}</div>
              <div class="v">{{ formatMemGi(resources?.allocatable?.memory) }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.resources.nodeCount') }}</div>
              <div class="v">{{ resources?.nodeCount ?? '-' }}</div>
            </div>
            <div class="kv">
              <div class="k">{{ t('clusters.detail.resources.podCount') }}</div>
              <div class="v">{{ resources?.podCount ?? '-' }}</div>
            </div>
          </div>
        </div>

        <div class="pad" v-else>
          <div v-if="data?.nodes && data.nodes.length" class="node-table">
            <div class="node-row node-head">
              <div>Node</div>
              <div>Ready</div>
              <div>Hostname</div>
              <div>Alloc CPU</div>
              <div>Req CPU</div>
              <div>Free CPU</div>
              <div>Alloc Mem</div>
              <div>Req Mem</div>
              <div>Free Mem</div>
              <div>UID</div>
            </div>

            <div v-for="(n, idx) in data.nodes" :key="nodeKey(n, idx)" class="node-row">
              <div class="mono">{{ n.name || '-' }}</div>
              <div>
                <span class="pill" :class="n.ready ? 'ready' : 'failed'">{{ n.ready ? 'Ready' : 'NotReady' }}</span>
              </div>
              <div class="mono">{{ getHostname(n.labels) }}</div>
              <div>{{ formatCpuCores(n.allocatable?.cpu) }}</div>
              <div>{{ formatCpuCores(n.requested?.cpu) }}</div>
              <div>{{ formatCpuCores(n.free?.cpu) }}</div>
              <div>{{ formatMemGi(n.allocatable?.memory) }}</div>
              <div>{{ formatMemGi(n.requested?.memory) }}</div>
              <div>{{ formatMemGi(n.free?.memory) }}</div>
              <div class="mono">{{ n.uid || '-' }}</div>
            </div>
          </div>
          <p v-else class="muted">{{ t('clusters.detail.nodesEmpty') }}</p>
        </div>
      </div>
    </template>

    <div class="card" v-else>
      <div class="pad muted">{{ t('clusters.detail.empty') }}</div>
    </div>

    <div v-if="showUpdate" class="modal-backdrop" @click.self="!updating && (showUpdate = false)">
      <div class="modal">
        <div class="modal-header">
          <h2>{{ t('clusters.update.title') }}</h2>
          <button class="icon-btn" :disabled="updating" @click="showUpdate = false" aria-label="Close">×</button>
        </div>

        <div class="modal-body">
          <div class="grid2">
            <label class="field">
              <span>{{ t('clusters.update.name') }}</span>
              <input v-model="updateForm.name" type="text" :placeholder="t('clusters.update.namePlaceholder')" />
            </label>

            <label class="field">
              <span>{{ t('clusters.update.provider') }}</span>
              <select v-model="updateForm.provider">
                <option value="AWS">AWS</option>
                <option value="ACK">ACK</option>
                <option value="TKE">TKE</option>
                <option value="VKE">VKE</option>
                <option value="GKE">GKE</option>
                <option value="Azure">Azure</option>
                <option value="Onprem">Onprem</option>
              </select>
            </label>
          </div>

          <label class="field">
            <span>{{ t('clusters.update.description') }}</span>
            <input v-model="updateForm.description" type="text" :placeholder="t('clusters.update.descriptionPlaceholder')" />
          </label>

          <label class="field">
            <span>{{ t('clusters.update.labels') }}</span>
            <textarea v-model="updateForm.labelsText" rows="4" :placeholder="t('clusters.update.labelsPlaceholder')" />
            <small class="hint">{{ t('clusters.update.labelsHint') }}</small>
          </label>

          <p v-if="updateError" class="error">{{ updateError }}</p>
        </div>

        <div class="modal-footer">
          <button class="btn" :disabled="updating" @click="showUpdate = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" :disabled="updating" @click="onSubmitUpdate">
            <span v-if="updating">{{ t('common.submitting') }}</span>
            <span v-else>{{ t('clusters.update.submit') }}</span>
          </button>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.page h1 {
  margin: 0 0 6px;
}
.topbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.actions {
  display: flex;
  gap: 10px;
}
.btn {
  border: 1px solid #e5e7eb;
  background: #fff;
  color: #111827;
  padding: 8px 12px;
  border-radius: 10px;
  cursor: pointer;
  font-weight: 600;
}
.btn.primary {
  background: #111827;
  border-color: #111827;
  color: #fff;
}

.node-table {
  display: grid;
  gap: 8px;
  overflow-x: auto;
}
.node-row {
  min-width: 980px;
  display: grid;
  grid-template-columns:
    160px /* node */
    90px /* ready */
    160px /* hostname */
    110px /* alloc cpu */
    110px /* req cpu */
    110px /* free cpu */
    110px /* alloc mem */
    110px /* req mem */
    110px /* free mem */
    1fr; /* uid */
  gap: 10px;
  align-items: center;
  padding: 10px;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  background: #fff;
}
.node-head {
  position: sticky;
  top: 0;
  z-index: 1;
  font-size: 12px;
  color: #6b7280;
  font-weight: 800;
  background: #f9fafb;
}
.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.icon-btn {
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 6px 10px;
}
.card {
  margin-top: 14px;
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  overflow: hidden;
}
.pad {
  padding: 14px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.kv {
  display: grid;
  gap: 4px;
}
.k {
  font-size: 12px;
  color: #6b7280;
  font-weight: 700;
}
.v {
  font-size: 14px;
  color: #111827;
}
.muted {
  color: #6b7280;
}
.error {
  color: #b91c1c;
  background: #fef2f2;
  border: 1px solid #fecaca;
  padding: 8px 10px;
  border-radius: 10px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
}
.pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  border: 1px solid;
}
.pill.importing {
  background: #fffbeb;
  border-color: #fcd34d;
  color: #92400e;
}
.pill.ready {
  background: #ecfdf5;
  border-color: #6ee7b7;
  color: #065f46;
}
.pill.notready,
.pill.failed {
  background: #fef2f2;
  border-color: #fecaca;
  color: #991b1b;
}
.labels {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.tag {
  font-size: 12px;
  border: 1px solid #e5e7eb;
  background: #f9fafb;
  color: #111827;
  padding: 2px 8px;
  border-radius: 999px;
}
.tabs {
  display: flex;
  gap: 6px;
  padding: 10px 10px 0;
  background: #fff;
}
.tab {
  border: 1px solid #e5e7eb;
  background: #fff;
  color: #111827;
  padding: 8px 12px;
  border-radius: 10px;
  cursor: pointer;
  font-weight: 700;
}
.tab.active {
  background: #111827;
  border-color: #111827;
  color: #fff;
}
.badge {
  margin-left: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 18px;
  min-width: 18px;
  padding: 0 6px;
  border-radius: 999px;
  background: #f3f4f6;
  color: #111827;
  font-size: 12px;
}
.code {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.4;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  padding: 12px;
  background: #0b1020;
  color: #e5e7eb;
  max-height: 420px;
  overflow: auto;
}
.node-list {
  display: grid;
  gap: 10px;
}
.node {
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  padding: 10px 12px;
}
.node-name {
  font-weight: 800;
}
.node-labels {
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.mini {
  display: inline-block;
  border: 1px solid #e5e7eb;
  border-radius: 999px;
  padding: 1px 8px;
  font-size: 12px;
  background: #f9fafb;
}

@media (max-width: 880px) {
  .grid {
    grid-template-columns: 1fr;
  }
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.modal {
  width: min(820px, 100%);
  background: #fff;
  border-radius: 14px;
  border: 1px solid #e5e7eb;
  overflow: hidden;
}
.modal-header {
  padding: 12px 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid #f3f4f6;
  background: #fafafa;
}
.modal-header h2 {
  margin: 0;
  font-size: 16px;
}
.modal-body {
  padding: 14px;
}
.modal-footer {
  padding: 12px 14px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  border-top: 1px solid #f3f4f6;
  background: #fafafa;
}
.grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.field {
  display: grid;
  gap: 6px;
  margin-bottom: 12px;
}
.field span {
  font-size: 12px;
  color: #374151;
  font-weight: 600;
}
input,
select,
textarea {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 10px 10px;
  font-size: 14px;
  font-family: inherit;
}
textarea {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
}
.hint {
  color: #6b7280;
}

@media (max-width: 880px) {
  .grid2 {
    grid-template-columns: 1fr;
  }
}
</style>
