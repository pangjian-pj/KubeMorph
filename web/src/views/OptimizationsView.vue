<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  createOptimizationPolicy,
  deleteOptimizationPolicy,
  listOptimizationPolicies,
  listTopologyConfigs,
  type OptimizationPolicyItem,
  type TopologyConfigItem,
} from '@/api/optimizations'

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const loadError = ref<string | null>(null)
const items = ref<OptimizationPolicyItem[]>([])
const total = ref(0)

const filters = ref({
  namespace: 'default',
  name: '',
})

const page = ref(1)
const pageSize = ref(10)

const totalPages = computed(() => Math.max(1, Math.ceil((total.value || 0) / pageSize.value)))

const showDeleteConfirm = ref(false)
const deleting = ref(false)
const deleteError = ref<string | null>(null)
const deleteTarget = ref<{ namespace: string; name: string } | null>(null)

// --- create modal ---
type GoalForm = {
  type: string
  weight: number
  sourceCity?: string
  topologyRef?: string
}

const showCreate = ref(false)
const creating = ref(false)
const createError = ref<string | null>(null)

const createNamespace = ref('default')
const createName = ref('')
const createEnabled = ref(true)
const createRunMode = ref<'Once' | 'Periodic'>('Once')
const createRebalancePoint = ref('')
const createStrategy = ref<'Conservative' | 'Aggressive' | 'Preview'>('Conservative')
const createThresholdPercent = ref<number>(0)

const createMatchLabels = ref<Array<{ key: string; value: string }>>([{ key: '', value: '' }])
const createGoals = ref<GoalForm[]>([])

const topologyOptions = ref<TopologyConfigItem[]>([])
const topologyLoading = ref(false)
const topologyError = ref<string | null>(null)
const topologyNamespace = ref('kubex-system')

const goalTypeOptions = ['Cost', 'Latency', 'Communication', 'Energy', 'Migration']
const sourceCityOptions = [
  'Hangzhou',
  'London',
  'Paris',
  'Shanghai',
  'Shenzhen',
  'Singapore',
  'Tokyo',
  'Toronto',
  'Washington',
  'Zhangjiakou',
]


function toCreate() {
  openCreate()
}

function toDetails(it: OptimizationPolicyItem) {
  router.push(`/optimizations/policies/${encodeURIComponent(it.namespace)}/${encodeURIComponent(it.name)}`)
}

function fmtTime(v?: string) {
  const s = (v || '').trim()
  if (!s) return '-'
  return s.replace('T', ' ').replace('Z', '')
}

function statusLabel(it: OptimizationPolicyItem) {
  return it.phase || (it.enabled ? 'Active' : 'Disabled')
}

function statusClass(it: OptimizationPolicyItem) {
  // reuse existing pill styles (e.g. active/failed/pending) with a few fallbacks
  const p = (it.phase || '').toLowerCase()
  if (p) return p
  return it.enabled ? 'active' : 'disabled'
}

function resetCreateForm() {
  createError.value = null
  createNamespace.value = filters.value.namespace.trim() || 'default'
  createName.value = ''
  createEnabled.value = true
  createRunMode.value = 'Once'
  createRebalancePoint.value = ''
  createStrategy.value = 'Conservative'
  createThresholdPercent.value = 0
  createMatchLabels.value = [{ key: '', value: '' }]
  createGoals.value = []
  topologyNamespace.value = 'kubex-system'
}

async function refreshTopology() {
  topologyLoading.value = true
  topologyError.value = null
  try {
  const resp = await listTopologyConfigs({ namespace: topologyNamespace.value.trim() || 'kubex-system' })
    topologyOptions.value = (resp.items || []).filter((x) => !!x)
  } catch (e: any) {
    topologyError.value = e?.response?.data?.error || e?.message || String(e)
    topologyOptions.value = []
  } finally {
    topologyLoading.value = false
  }
}

function openCreate() {
  resetCreateForm()
  showCreate.value = true
  refreshTopology()
}

function addCreateLabelRow() {
  createMatchLabels.value.push({ key: '', value: '' })
}

function removeCreateLabelRow(idx: number) {
  createMatchLabels.value.splice(idx, 1)
  if (createMatchLabels.value.length === 0) createMatchLabels.value.push({ key: '', value: '' })
}

function newGoal(): GoalForm {
  return { type: 'Cost', weight: 0 }
}

function addCreateGoal() {
  createGoals.value.push(newGoal())
}

function removeCreateGoal(idx: number) {
  createGoals.value.splice(idx, 1)
}

function buildCreateMatchLabels() {
  const out: Record<string, string> = {}
  for (const kv of createMatchLabels.value) {
    const k = (kv.key || '').trim()
    const v = (kv.value || '').trim()
    if (!k) continue
    out[k] = v
  }
  return out
}

function normalizeCreateGoals() {
  return createGoals.value
    .map((g) => ({
      type: (g.type || '').trim(),
      weight: Number.isFinite(g.weight) ? g.weight : 0,
      sourceCity: (g.sourceCity || '').trim() || undefined,
      topologyRef: (g.topologyRef || '').trim() || undefined,
    }))
    .filter((g) => !!g.type)
}

const createTotalWeight = computed(() => normalizeCreateGoals().reduce((sum, g) => sum + (g.weight || 0), 0))

function autoNormalizeCreateWeights() {
  const gs = normalizeCreateGoals()
  if (gs.length === 0) return
  const sum = gs.reduce((s, x) => s + (x.weight || 0), 0)
  if (sum <= 0) {
    const w = 1 / gs.length
    for (const g of createGoals.value) g.weight = w
    return
  }
  for (const g of createGoals.value) g.weight = (g.weight || 0) / sum
}

function buildPolicyForCreate() {
  const ns = createNamespace.value.trim() || 'default'
  const nm = createName.value.trim()
  if (!nm) throw new Error('name is required')

  // if goals provided, weights must sum to 1
  if (createGoals.value.length > 0 && Math.abs(createTotalWeight.value - 1) > 1e-6) {
    throw new Error(`goals weight sum must be 1 (current=${createTotalWeight.value.toFixed(6)})`)
  }

  const obj: any = {
    apiVersion: 'core.kubex.io/v1alpha1',
    kind: 'OptimizationPolicy',
    metadata: { name: nm, namespace: ns },
    spec: {
      enabled: createEnabled.value,
      runMode: createRunMode.value,
      strategy: createStrategy.value,
      targetSelector: { matchLabels: buildCreateMatchLabels() },
      optimizationGoals: normalizeCreateGoals().map((g) => {
        const out: any = { type: g.type, weight: g.weight }
        if (g.type === 'Latency' && g.sourceCity) out.sourceCity = g.sourceCity
        if (g.type === 'Communication' && g.topologyRef) out.topologyRef = g.topologyRef
        return out
      }),
    },
  }

  if (createRunMode.value === 'Periodic') {
    obj.spec.rebalancePoint = (createRebalancePoint.value || '').trim()
  }

  if (createStrategy.value === 'Conservative') {
    obj.spec.improvementThresholdPercent = Math.max(0, Math.min(100, Number(createThresholdPercent.value || 0)))
  }

  return obj
}

async function onConfirmCreate(goToDetail: boolean) {
  creating.value = true
  createError.value = null
  try {
    const policy = buildPolicyForCreate()
    await createOptimizationPolicy({ policy })
    showCreate.value = false
    await refreshList()
    if (goToDetail) {
      router.push(`/optimizations/policies/${encodeURIComponent(policy.metadata.namespace)}/${encodeURIComponent(policy.metadata.name)}`)
    }
  } catch (e: any) {
    createError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    creating.value = false
  }
}

async function refreshList() {
  loading.value = true
  loadError.value = null
  try {
    const resp = await listOptimizationPolicies({
      namespace: filters.value.namespace.trim() || undefined,
      name: filters.value.name.trim() || undefined,
      page: page.value,
      pageSize: pageSize.value,
    })
    items.value = resp.items || []
    total.value = resp.total || 0
    if (page.value > totalPages.value) page.value = totalPages.value
  } catch (e: any) {
    loadError.value = e?.response?.data?.error || e?.message || String(e)
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function onSearch() {
  page.value = 1
  refreshList()
}

watch([page, pageSize], () => refreshList())
onMounted(() => refreshList())

function openDelete(it: OptimizationPolicyItem) {
  deleteError.value = null
  deleteTarget.value = { namespace: it.namespace, name: it.name }
  showDeleteConfirm.value = true
}

async function onConfirmDelete() {
  if (!deleteTarget.value) return
  deleting.value = true
  deleteError.value = null
  try {
    await deleteOptimizationPolicy({ namespace: deleteTarget.value.namespace, name: deleteTarget.value.name })
    showDeleteConfirm.value = false
    deleteTarget.value = null
    await refreshList()
  } catch (e: any) {
    deleteError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    deleting.value = false
  }
}

</script>

<template>
  <section class="page">
    <div class="topbar">
      <div>
        <h1>{{ t('optimizations.title') }}</h1>
        <p class="muted">{{ t('optimizations.desc') }}</p>
      </div>
      <div class="actions">
        <button class="btn" :disabled="loading" @click="refreshList">Refresh</button>
  <button class="btn primary" @click="toCreate">Create OptimizationPolicy</button>
      </div>
    </div>

    <div class="card">
      <div class="pad">
        <div class="filters">
          <label class="field" style="margin: 0">
            <span>Namespace</span>
            <input v-model="filters.namespace" placeholder="default" @keydown.enter="onSearch" />
          </label>
          <label class="field" style="margin: 0">
            <span>Name</span>
            <input v-model="filters.name" placeholder="policy" @keydown.enter="onSearch" />
          </label>
          <div class="filters-actions">
            <button class="btn" :disabled="loading" @click="onSearch">Search</button>
          </div>
        </div>

        <p v-if="loadError" class="error" style="margin-top: 12px">{{ loadError }}</p>

        <div class="table-wrap" style="margin-top: 12px">
          <table class="table">
            <thead>
              <tr>
                <th>Status</th>
                <th>Name</th>
                <th>Namespace</th>
                <th>RunMode</th>
                <th>LastRun</th>
                <th class="actions-col">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="loading">
                <td colspan="6" class="muted">Loading...</td>
              </tr>
              <tr v-else-if="items.length === 0">
                <td colspan="6" class="muted">No data</td>
              </tr>
              <tr v-else v-for="it in items" :key="`${it.namespace}/${it.name}`">
                <td>
                  <span class="pill" :class="statusClass(it)">{{ statusLabel(it) }}</span>
                </td>
                <td class="mono">{{ it.name }}</td>
                <td class="mono">{{ it.namespace }}</td>
                <td class="mono">{{ it.runMode || '-' }}</td>
                <td class="mono">{{ fmtTime(it.lastEvaluationTime) }}</td>
                <td class="actions-col">
                  <button class="btn" @click="toDetails(it)">Details</button>
                  <button class="btn danger" @click="openDelete(it)">Delete</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="pager">
          <div class="muted">Total: {{ total }}</div>
          <div class="pager-actions">
            <button class="btn" :disabled="loading || page <= 1" @click="page--">Prev</button>
            <div class="mono">Page {{ page }} / {{ totalPages }}</div>
            <button class="btn" :disabled="loading || page >= totalPages" @click="page++">Next</button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="showDeleteConfirm" class="modal-backdrop" @click.self="!deleting && (showDeleteConfirm = false)">
      <div class="modal">
        <div class="modal-header">
          <h2>Delete OptimizationPolicy</h2>
          <button class="icon-btn" :disabled="deleting" @click="showDeleteConfirm = false" aria-label="Close">×</button>
        </div>
        <div class="modal-body">
          <p>
            Delete <b class="mono">{{ deleteTarget?.namespace }}/{{ deleteTarget?.name }}</b> ?
          </p>
          <p v-if="deleteError" class="error">{{ deleteError }}</p>
        </div>
        <div class="modal-footer">
          <button class="btn" :disabled="deleting" @click="showDeleteConfirm = false">{{ t('common.cancel') }}</button>
          <button class="btn danger" :disabled="deleting" @click="onConfirmDelete">Delete</button>
        </div>
      </div>
    </div>

    <div v-if="showCreate" class="modal-backdrop" @click.self="!creating && (showCreate = false)">
      <div class="modal" style="max-width: 980px">
        <div class="modal-header">
          <h2>Create OptimizationPolicy</h2>
          <button class="icon-btn" :disabled="creating" @click="showCreate = false" aria-label="Close">×</button>
        </div>
        <div class="modal-body">
          <p v-if="createError" class="error">{{ createError }}</p>

          <div class="grid2">
            <label class="field" style="margin: 0">
              <span>Namespace</span>
              <input v-model="createNamespace" placeholder="default" @change="refreshTopology" />
            </label>
            <label class="field" style="margin: 0">
              <span>Name</span>
              <input v-model="createName" placeholder="policy-001" />
            </label>
          </div>

          <div class="grid2" style="margin-top: 12px">
            <label class="field" style="margin: 0">
              <span>Enabled</span>
              <select v-model="createEnabled">
                <option :value="true">true</option>
                <option :value="false">false</option>
              </select>
            </label>
            <label class="field" style="margin: 0">
              <span>RunMode</span>
              <select v-model="createRunMode">
                <option value="Once">Once</option>
                <option value="Periodic">Periodic</option>
              </select>
            </label>
          </div>

          <label v-if="createRunMode === 'Periodic'" class="field" style="margin-top: 12px">
            <span>Schedule (rebalancePoint)</span>
            <input v-model="createRebalancePoint" placeholder="e.g. */5 * * * *" />
          </label>

          <div class="grid2" style="margin-top: 12px">
            <label class="field" style="margin: 0">
              <span>Strategy</span>
              <select v-model="createStrategy">
                <option value="Preview">Preview</option>
                <option value="Conservative">Conservative</option>
                <option value="Aggressive">Aggressive</option>
              </select>
            </label>
            <label v-if="createStrategy === 'Conservative'" class="field" style="margin: 0">
              <span>Threshold (%)</span>
              <input v-model.number="createThresholdPercent" type="number" min="0" max="100" step="1" />
            </label>
          </div>

          <h3 style="margin: 14px 0 6px">LabelSelector (matchLabels)</h3>
          <div class="kv">
            <div class="kv-row" v-for="(kv, idx) in createMatchLabels" :key="idx">
              <input v-model="kv.key" placeholder="key" />
              <input v-model="kv.value" placeholder="value" />
              <button class="btn danger" :disabled="creating" @click="removeCreateLabelRow(idx)">Remove</button>
            </div>
            <button class="btn" :disabled="creating" @click="addCreateLabelRow">Add label</button>
          </div>

          <h3 style="margin: 14px 0 6px">Goals</h3>
          <p class="muted" style="margin: 0 0 10px">
            Total weight: <b>{{ createTotalWeight.toFixed(6) }}</b> (must be 1)
          </p>

          <div class="goals">
            <div class="goal" v-for="(g, idx) in createGoals" :key="idx">
              <div class="goal-row">
                <label class="field" style="margin: 0">
                  <span>Type</span>
                  <select v-model="g.type">
                    <option v-for="opt in goalTypeOptions" :key="opt" :value="opt">{{ opt }}</option>
                  </select>
                </label>
                <label class="field" style="margin: 0">
                  <span>Weight</span>
                  <input v-model.number="g.weight" type="number" step="0.01" min="0" />
                </label>
                <div class="goal-actions">
                  <button class="btn" :disabled="creating" @click="autoNormalizeCreateWeights">Normalize</button>
                  <button class="btn danger" :disabled="creating" @click="removeCreateGoal(idx)">Remove</button>
                </div>
              </div>

              <div class="goal-row" v-if="g.type === 'Latency'" style="margin-top: 10px">
                <label class="field" style="margin: 0">
                  <span>sourceCity</span>
                  <select v-model="g.sourceCity">
                    <option value="">(empty)</option>
                    <option v-for="c in sourceCityOptions" :key="c" :value="c">{{ c }}</option>
                  </select>
                </label>
              </div>

              <div class="goal-row" v-if="g.type === 'Communication'" style="margin-top: 10px">
                <label class="field" style="margin: 0">
                  <span>Topology Namespace</span>
                  <input v-model="topologyNamespace" placeholder="kubex-system" @change="refreshTopology" />
                </label>
                <label class="field" style="margin: 0">
                  <span>topologyRef</span>
                  <select v-model="g.topologyRef">
                    <option value="">(none)</option>
                    <option v-for="opt in topologyOptions" :key="opt.namespace + '/' + opt.name" :value="opt.name">
                      {{ opt.name }}
                    </option>
                  </select>
                </label>
                <span class="muted" style="align-self: end" v-if="topologyLoading">Loading topologies...</span>
                <span class="muted" style="align-self: end" v-else-if="topologyError">{{ topologyError }}</span>
              </div>
            </div>

            <button class="btn" :disabled="creating" @click="addCreateGoal">Add goal</button>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn" :disabled="creating" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button class="btn" :disabled="creating" @click="onConfirmCreate(false)">Create</button>
          <button class="btn primary" :disabled="creating" @click="onConfirmCreate(true)">Create & Open</button>
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
.filters {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  gap: 12px;
  align-items: end;
}
.filters-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.table-wrap {
  overflow: auto;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
}
.table {
  width: 100%;
  border-collapse: collapse;
  min-width: 860px;
}
.table th,
.table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f3f4f6;
  text-align: left;
  font-size: 14px;
}
.table th {
  font-size: 12px;
  color: #6b7280;
  font-weight: 700;
  background: #fafafa;
}
.num {
  text-align: right;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New",
    monospace;
}
.actions-col {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  white-space: nowrap;
}
.pager {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
}
.pager-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  border: 1px solid;
  text-transform: none;
}
.pill.pending {
  background: #f3f4f6;
  border-color: #e5e7eb;
  color: #374151;
}
.pill.progressing {
  background: #eff6ff;
  border-color: #bfdbfe;
  color: #1d4ed8;
}
.pill.running {
  background: #ecfdf5;
  border-color: #6ee7b7;
  color: #065f46;
}
.pill.degraded,
.pill.failed {
  background: #fef2f2;
  border-color: #fecaca;
  color: #991b1b;
}
.pill.scaling {
  background: #fffbeb;
  border-color: #fcd34d;
  color: #92400e;
}
.pill.deleting {
  background: #fff7ed;
  border-color: #fed7aa;
  color: #9a3412;
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
.btn.danger {
  background: #fff;
  border-color: #b91c1c;
  color: #b91c1c;
}
.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
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
textarea {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 10px 10px;
  font-size: 14px;
  font-family: inherit;
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

select {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 10px 10px;
  font-size: 14px;
  font-family: inherit;
  background: #fff;
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(17, 24, 39, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  z-index: 50;
}
.modal {
  width: 100%;
  max-height: calc(100vh - 48px);
  overflow: auto;
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 14px;
  box-shadow: 0 15px 40px rgba(0, 0, 0, 0.25);
}
.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 14px;
  border-bottom: 1px solid #f3f4f6;
}
.modal-header h2 {
  margin: 0;
  font-size: 16px;
}
.modal-body {
  padding: 14px;
}
.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  padding: 14px;
  border-top: 1px solid #f3f4f6;
  background: #fafafa;
}
.icon-btn {
  border: 1px solid #e5e7eb;
  background: #fff;
  width: 32px;
  height: 32px;
  border-radius: 10px;
  cursor: pointer;
  font-weight: 800;
  line-height: 1;
}

.grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.kv {
  display: grid;
  gap: 10px;
}
.kv-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  gap: 10px;
}

.goals {
  display: grid;
  gap: 10px;
}
.goal {
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  padding: 12px;
  background: #fff;
}
.goal-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  gap: 12px;
  align-items: end;
}
.goal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 900px) {
  .grid2 {
    grid-template-columns: 1fr;
  }
  .filters {
    grid-template-columns: 1fr;
  }
  .kv-row {
    grid-template-columns: 1fr;
  }
  .goal-row {
    grid-template-columns: 1fr;
  }
  .goal-actions {
    justify-content: flex-start;
  }
}
</style>
