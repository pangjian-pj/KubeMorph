<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { load as loadYaml, dump as dumpYaml } from 'js-yaml'

import {
  getOptimizationPolicy,
  updateOptimizationPolicy,
  listReOrchestrationPlans,
  listTopologyConfigs,
  type ReOrchestrationPlanItem,
  type TopologyConfigItem,
} from '@/api/optimizations'

const route = useRoute()
const router = useRouter()

const namespace = computed(() => String(route.params.namespace || 'default'))
const name = computed(() => String(route.params.name || ''))

const loading = ref(false)
const error = ref<string | null>(null)

const saving = ref(false)
const saveError = ref<string | null>(null)
const saveOk = ref<string | null>(null)

const policy = ref<any>(null)
const yamlText = ref('')

type GoalForm = {
  type: string
  weight: number
  sourceCity?: string
  topologyRef?: string
}

const activeTab = ref<'details' | 'yaml'>('details')

const formError = ref<string | null>(null)

const updateOpen = ref(false)
const updateActiveTab = ref<'form' | 'yaml'>('form')

const matchLabels = ref<Array<{ key: string; value: string }>>([])
const goals = ref<GoalForm[]>([])

const runMode = ref<'Once' | 'Periodic'>('Once')
const rebalancePoint = ref('')
const strategy = ref<'Conservative' | 'Aggressive' | 'Preview'>('Conservative')
const thresholdPercent = ref<number>(0)
const enabled = ref(true)

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

function goBack() {
  router.push('/optimizations')
}

function parseMatchLabels(obj: any) {
  const ml = obj?.spec?.targetSelector?.matchLabels
  const out: Array<{ key: string; value: string }> = []
  if (ml && typeof ml === 'object') {
    for (const k of Object.keys(ml)) {
      out.push({ key: k, value: String(ml[k] ?? '') })
    }
  }
  matchLabels.value = out.length ? out : [{ key: '', value: '' }]
}

function parseGoals(obj: any) {
  const gs = obj?.spec?.optimizationGoals
  const out: GoalForm[] = []
  if (Array.isArray(gs)) {
    for (const g of gs) {
      out.push({
        type: String(g?.type || ''),
        weight: typeof g?.weight === 'number' ? g.weight : 0,
        sourceCity: g?.sourceCity ? String(g.sourceCity) : undefined,
        topologyRef: g?.topologyRef ? String(g.topologyRef) : undefined,
      })
    }
  }
  goals.value = out.length ? out : []
}

function parseMainSpec(obj: any) {
  enabled.value = !!obj?.spec?.enabled
  runMode.value = (obj?.spec?.runMode === 'Periodic' ? 'Periodic' : 'Once')
  rebalancePoint.value = String(obj?.spec?.rebalancePoint || '')
  const s = String(obj?.spec?.strategy || 'Conservative') as any
  strategy.value = (s === 'Aggressive' || s === 'Preview' || s === 'Conservative') ? s : 'Conservative'
  thresholdPercent.value = typeof obj?.spec?.improvementThresholdPercent === 'number' ? obj.spec.improvementThresholdPercent : 0
}

function syncFormFromPolicy(obj: any) {
  parseMainSpec(obj)
  parseMatchLabels(obj)
  parseGoals(obj)
}

function buildMatchLabels() {
  const out: Record<string, string> = {}
  for (const kv of matchLabels.value) {
    const k = (kv.key || '').trim()
    const v = (kv.value || '').trim()
    if (!k) continue
    out[k] = v
  }
  return out
}

function normalizeGoals() {
  return goals.value
    .map((g) => ({
      type: (g.type || '').trim(),
      weight: Number.isFinite(g.weight) ? g.weight : 0,
      sourceCity: (g.sourceCity || '').trim() || undefined,
      topologyRef: (g.topologyRef || '').trim() || undefined,
    }))
    .filter((g) => !!g.type)
}

const totalWeight = computed(() => {
  return normalizeGoals().reduce((sum, g) => sum + (Number.isFinite(g.weight) ? g.weight : 0), 0)
})

function newGoal(): GoalForm {
  return { type: 'Cost', weight: 0 }
}

function addLabelRow() {
  matchLabels.value.push({ key: '', value: '' })
}

function removeLabelRow(idx: number) {
  matchLabels.value.splice(idx, 1)
  if (matchLabels.value.length === 0) matchLabels.value.push({ key: '', value: '' })
}

function addGoal() {
  goals.value.push(newGoal())
}

function removeGoal(idx: number) {
  goals.value.splice(idx, 1)
}

function autoNormalizeWeights() {
  const gs = normalizeGoals()
  if (gs.length === 0) return
  // if all zeros, distribute evenly
  const sum = gs.reduce((s, x) => s + (x.weight || 0), 0)
  if (sum <= 0) {
    const w = 1 / gs.length
    for (const g of goals.value) g.weight = w
    return
  }
  // normalize to sum=1
  for (const g of goals.value) {
    g.weight = (g.weight || 0) / sum
  }
}

function buildPolicyFromForm() {
  const base = policy.value ? JSON.parse(JSON.stringify(policy.value)) : null
  if (!base) throw new Error('policy not loaded')

  base.spec = base.spec || {}
  base.spec.enabled = enabled.value
  base.spec.runMode = runMode.value
  if (runMode.value === 'Periodic') {
    base.spec.rebalancePoint = (rebalancePoint.value || '').trim()
  } else {
    delete base.spec.rebalancePoint
  }

  base.spec.strategy = strategy.value
  if (strategy.value === 'Conservative') {
    base.spec.improvementThresholdPercent = Math.max(0, Math.min(100, Number(thresholdPercent.value || 0)))
  } else {
    delete base.spec.improvementThresholdPercent
  }

  base.spec.targetSelector = { matchLabels: buildMatchLabels() }
  base.spec.optimizationGoals = normalizeGoals().map((g) => {
    const out: any = { type: g.type, weight: g.weight }
    if (g.type === 'Latency' && g.sourceCity) out.sourceCity = g.sourceCity
    if (g.type === 'Communication' && g.topologyRef) out.topologyRef = g.topologyRef
    return out
  })
  return base
}

const plans = ref<ReOrchestrationPlanItem[]>([])
const plansError = ref<string | null>(null)

function toPlan(it: ReOrchestrationPlanItem) {
  router.push(`/optimizations/plans/${encodeURIComponent(it.namespace)}/${encodeURIComponent(it.name)}`)
}

async function refresh() {
  if (!name.value) return
  loading.value = true
  error.value = null
  saveOk.value = null
  try {
    const resp = await getOptimizationPolicy({ namespace: namespace.value, name: name.value })
    policy.value = resp.policy
    yamlText.value = dumpYaml(resp.policy)
  syncFormFromPolicy(resp.policy)
  } catch (e: any) {
    error.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    loading.value = false
  }
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

async function refreshPlans() {
  plansError.value = null
  try {
    const resp = await listReOrchestrationPlans({
      namespace: namespace.value,
      policyName: name.value,
      page: 1,
      pageSize: 50,
    })
    plans.value = (resp.items || []).filter((x) => !!x)
  } catch (e: any) {
    plansError.value = e?.response?.data?.error || e?.message || String(e)
    plans.value = []
  }
}

async function onSave() {
  saving.value = true
  saveError.value = null
  saveOk.value = null
  formError.value = null
  try {
    let obj: any
    if (updateActiveTab.value === 'yaml') {
      obj = loadYaml(yamlText.value) as any
      if (!obj || typeof obj !== 'object') throw new Error('invalid yaml')
    } else {
      if (goals.value.length > 0 && Math.abs(totalWeight.value - 1) > 1e-6) {
        throw new Error(`goals weight sum must be 1 (current=${totalWeight.value.toFixed(6)})`)
      }
      obj = buildPolicyFromForm()
      yamlText.value = dumpYaml(obj)
    }

    await updateOptimizationPolicy({ policy: obj })
    saveOk.value = 'updated'
    updateOpen.value = false
    await refresh()
  } catch (e: any) {
    const msg = e?.response?.data?.error || e?.message || String(e)
    saveError.value = msg
    formError.value = msg
  } finally {
    saving.value = false
  }
}

watch(
  () => updateActiveTab.value,
  (tab) => {
    if (tab === 'form') {
      // if yaml was edited, try to parse it back into form
      try {
        const obj = loadYaml(yamlText.value) as any
        if (obj && typeof obj === 'object') {
          policy.value = obj
          syncFormFromPolicy(obj)
        }
      } catch {
        // ignore parse error here; user will see it on save
      }
    }
  },
)

watch(
  () => updateOpen.value,
  (open) => {
    if (open) {
      // opening modal: reset tab + error and re-sync form/yaml from loaded policy
      updateActiveTab.value = 'form'
      formError.value = null
      saveError.value = null
      saveOk.value = null
      if (policy.value) {
        yamlText.value = dumpYaml(policy.value)
        syncFormFromPolicy(policy.value)
      }
      refreshTopology()
    }
  },
)

onMounted(() => {
  refresh()
  refreshPlans()
  refreshTopology()
})
</script>

<template>
  <section class="page">
    <div class="topbar">
      <div>
        <h1 class="mono">{{ namespace }}/{{ name }}</h1>
        <p class="muted">OptimizationPolicy</p>
      </div>
      <div class="actions">
  <button class="btn" :disabled="loading" @click="goBack">Back</button>
  <button class="btn" :disabled="loading" @click="refresh">Refresh</button>
  <button class="btn primary" :disabled="loading" @click="updateOpen = true">Update</button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="saveOk" class="ok">{{ saveOk }}</p>
    <p v-if="saveError" class="error">{{ saveError }}</p>

    <div class="card" style="margin-top: 14px">
      <div class="pad">
        <h3 style="margin-top: 0">Plans</h3>
        <p v-if="plansError" class="error">{{ plansError }}</p>
        <div class="table-wrap" v-if="plans.length">
          <table class="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Phase</th>
                <th class="num">Moves</th>
                <th>Created</th>
                <th class="actions-col">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in plans" :key="`${p.namespace}/${p.name}`">
                <td class="mono">{{ p.name }}</td>
                <td>{{ p.phase || '-' }}</td>
                <td class="num mono">{{ p.moves }}</td>
                <td class="mono">{{ (p.creationTimestamp || '').replace('T', ' ').replace('Z', '') || '-' }}</td>
                <td class="actions-col"><button class="btn" @click="toPlan(p)">Details</button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="muted">No plans</p>
      </div>
    </div>

    <div class="card">
      <div class="pad">
        <div class="tabs">
          <button class="tab" :class="{ active: activeTab === 'details' }" @click="activeTab = 'details'">Details</button>
          <button class="tab" :class="{ active: activeTab === 'yaml' }" @click="activeTab = 'yaml'">YAML</button>
        </div>

        <div v-if="activeTab === 'details'">
          <h3 style="margin-top: 0">Basic</h3>
          <div class="grid2">
            <div class="field ro">
              <span>Enabled</span>
              <div class="ro-value mono">{{ enabled ? 'true' : 'false' }}</div>
            </div>
            <div class="field ro">
              <span>RunMode</span>
              <div class="ro-value mono">{{ runMode }}</div>
            </div>
          </div>

          <div v-if="runMode === 'Periodic'" class="field ro">
            <span>Schedule (rebalancePoint)</span>
            <div class="ro-value mono">{{ rebalancePoint || '-' }}</div>
          </div>

          <h3>Strategy</h3>
          <div class="grid2">
            <div class="field ro">
              <span>Strategy</span>
              <div class="ro-value mono">{{ strategy }}</div>
            </div>
            <div class="field ro">
              <span>Threshold (%)</span>
              <div class="ro-value mono">{{ strategy === 'Conservative' ? (thresholdPercent ?? 0) : '-' }}</div>
            </div>
          </div>

          <h3>LabelSelector</h3>
          <div class="kv" v-if="matchLabels.length && matchLabels.some((x) => (x.key || '').trim())">
            <div class="kv-row ro" v-for="(kv, idx) in matchLabels" :key="idx">
              <div class="ro-value mono">{{ (kv.key || '-').trim() }}</div>
              <div class="ro-value mono">{{ (kv.value || '-').trim() }}</div>
            </div>
          </div>
          <p v-else class="muted">No matchLabels</p>

          <h3>Goals</h3>
          <p class="muted" style="margin-top: 6px">Total weight: <b>{{ totalWeight.toFixed(6) }}</b></p>

          <div class="goals" v-if="goals.length">
            <div class="goal" v-for="(g, idx) in goals" :key="idx">
              <div class="goal-ro">
                <div class="field ro" style="margin: 0">
                  <span>Type</span>
                  <div class="ro-value mono">{{ g.type }}</div>
                </div>
                <div class="field ro" style="margin: 0">
                  <span>Weight</span>
                  <div class="ro-value mono">{{ Number.isFinite(g.weight) ? g.weight : 0 }}</div>
                </div>
                <div class="field ro" style="margin: 0" v-if="g.type === 'Latency'">
                  <span>sourceCity</span>
                  <div class="ro-value mono">{{ g.sourceCity || '-' }}</div>
                </div>
                <div class="field ro" style="margin: 0" v-if="g.type === 'Communication'">
                  <span>topologyRef</span>
                  <div class="ro-value mono">{{ g.topologyRef || '-' }}</div>
                </div>
              </div>
            </div>
          </div>
          <p v-else class="muted">No goals</p>
        </div>

        <div v-else>
          <h3 style="margin-top: 0">Policy YAML</h3>
          <textarea v-model="yamlText" rows="22" class="mono" style="width: 100%" readonly />
        </div>
      </div>
    </div>

    <div v-if="updateOpen" class="modal-backdrop" @click.self="updateOpen = false">
      <div class="modal">
        <div class="modal-header">
          <div>
            <div class="modal-title">Update OptimizationPolicy</div>
            <div class="muted mono">{{ namespace }}/{{ name }}</div>
          </div>
          <button class="btn" @click="updateOpen = false">Close</button>
        </div>

        <p v-if="formError" class="error" style="margin-top: 10px">{{ formError }}</p>

        <div class="tabs" style="margin-top: 12px">
          <button class="tab" :class="{ active: updateActiveTab === 'form' }" @click="updateActiveTab = 'form'">Form</button>
          <button class="tab" :class="{ active: updateActiveTab === 'yaml' }" @click="updateActiveTab = 'yaml'">YAML</button>
        </div>

        <div v-if="updateActiveTab === 'form'" class="form">
          <h3 style="margin-top: 0">Basic</h3>
          <div class="grid2">
            <label class="field">
              <span>Enabled</span>
              <select v-model="enabled">
                <option :value="true">true</option>
                <option :value="false">false</option>
              </select>
            </label>
            <label class="field">
              <span>RunMode</span>
              <select v-model="runMode">
                <option value="Once">Once</option>
                <option value="Periodic">Periodic</option>
              </select>
            </label>
          </div>

          <label v-if="runMode === 'Periodic'" class="field">
            <span>Schedule (rebalancePoint)</span>
            <input v-model="rebalancePoint" placeholder="e.g. */5 * * * *" />
          </label>

          <h3>Strategy</h3>
          <div class="grid2">
            <label class="field">
              <span>Strategy</span>
              <select v-model="strategy">
                <option value="Preview">Preview</option>
                <option value="Conservative">Conservative</option>
                <option value="Aggressive">Aggressive</option>
              </select>
            </label>
            <label v-if="strategy === 'Conservative'" class="field">
              <span>Threshold (%)</span>
              <input v-model.number="thresholdPercent" type="number" min="0" max="100" step="1" />
            </label>
          </div>

          <h3>LabelSelector (matchLabels)</h3>
          <div class="kv">
            <div class="kv-row" v-for="(kv, idx) in matchLabels" :key="idx">
              <input v-model="kv.key" placeholder="key" />
              <input v-model="kv.value" placeholder="value" />
              <button class="btn danger" @click="removeLabelRow(idx)">Remove</button>
            </div>
            <button class="btn" @click="addLabelRow">Add label</button>
          </div>

          <h3>Goals</h3>
          <p class="muted" style="margin-top: 6px">Total weight: <b>{{ totalWeight.toFixed(6) }}</b> (must be 1)</p>

          <div class="goals">
            <div class="goal" v-for="(g, idx) in goals" :key="idx">
              <div class="goal-row">
                <label class="field" style="margin: 0">
                  <span>Type</span>
                  <select v-model="g.type">
                    <option v-for="opt in goalTypeOptions" :key="opt" :value="opt">{{ opt }}</option>
                  </select>
                </label>

                <label class="field" style="margin: 0">
                  <span>Weight</span>
                  <input v-model.number="g.weight" type="number" min="0" max="1" step="0.01" />
                </label>

                <div class="goal-actions">
                  <button class="btn" @click="autoNormalizeWeights">Normalize</button>
                  <button class="btn danger" @click="removeGoal(idx)">Remove</button>
                </div>
              </div>

              <div class="goal-row" style="margin-top: 10px" v-if="g.type === 'Latency'">
                <label class="field" style="margin: 0">
                  <span>sourceCity</span>
                  <select v-model="g.sourceCity">
                    <option value="">(empty)</option>
                    <option v-for="c in sourceCityOptions" :key="c" :value="c">{{ c }}</option>
                  </select>
                </label>
              </div>

              <div class="goal-row" style="margin-top: 10px" v-if="g.type === 'Communication'">
                <label class="field" style="margin: 0">
                  <span>Topology Namespace</span>
                  <input v-model="topologyNamespace" placeholder="kubex-system" @change="refreshTopology" />
                </label>
                <label class="field" style="margin: 0">
                  <span>topologyRef</span>
                  <select v-model="g.topologyRef" :disabled="topologyLoading">
                    <option value="">(empty)</option>
                    <option v-for="t in topologyOptions" :key="`${t.namespace}/${t.name}`" :value="t.name">
                      {{ t.name }}
                    </option>
                  </select>
                </label>
                <p v-if="topologyError" class="error" style="margin-top: 8px">{{ topologyError }}</p>
              </div>
            </div>

            <button class="btn" @click="addGoal">Add goal</button>
          </div>
        </div>

        <div v-else>
          <h3 style="margin-top: 0">Policy YAML</h3>
          <textarea v-model="yamlText" rows="18" class="mono" style="width: 100%" />
        </div>

        <div class="modal-footer">
          <button class="btn" :disabled="saving" @click="updateOpen = false">Cancel</button>
          <button class="btn primary" :disabled="saving || loading" @click="onSave">Save</button>
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

.card {
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

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
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
  border-color: #fecaca;
  color: #b91c1c;
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.table-wrap {
  overflow: auto;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
}

.table {
  width: 100%;
  border-collapse: collapse;
  min-width: 760px;
}

.table th,
.table td {
  border-bottom: 1px solid #f3f4f6;
  padding: 10px 12px;
  text-align: left;
  vertical-align: middle;
  white-space: nowrap;
}

.table thead th {
  font-size: 12px;
  color: #374151;
  background: #f9fafb;
  font-weight: 800;
}

.table tbody tr:hover {
  background: #fafafa;
}

.num {
  text-align: right !important;
}

.actions-col {
  width: 120px;
  text-align: right;
}

.ok {
  color: #065f46;
}

.tabs {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}
.tab {
  border: 1px solid #e5e7eb;
  background: #fff;
  padding: 8px 10px;
  border-radius: 10px;
  cursor: pointer;
  font-weight: 700;
}
.tab.active {
  background: #111827;
  border-color: #111827;
  color: #fff;
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
textarea,
select {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 10px 10px;
  font-size: 14px;
  font-family: inherit;
  background: #fff;
}

.kv-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  gap: 10px;
  align-items: end;
  margin-bottom: 10px;
}

.kv-row.ro {
  grid-template-columns: 1fr 1fr;
  align-items: start;
}

.ro-value {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 10px 10px;
  background: #f9fafb;
  font-size: 14px;
}

.goal {
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  padding: 12px;
  background: #fafafa;
  margin-bottom: 12px;
}
.goal-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  gap: 12px;
  align-items: end;
}
.goal-ro {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.goal-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(17, 24, 39, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  z-index: 50;
}

.modal {
  width: min(960px, 100%);
  max-height: min(92vh, 900px);
  overflow: auto;
  background: #fff;
  border-radius: 14px;
  border: 1px solid #e5e7eb;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.24);
  padding: 14px;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 10px;
}

.modal-title {
  font-weight: 800;
  font-size: 16px;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid #e5e7eb;
}

@media (max-width: 980px) {
  .grid2 {
    grid-template-columns: 1fr;
  }
  .kv-row {
    grid-template-columns: 1fr;
  }
  .goal-row {
    grid-template-columns: 1fr;
  }
  .goal-ro {
    grid-template-columns: 1fr;
  }
}
</style>
