<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { dump as dumpYaml } from 'js-yaml'

import { createOptimizationPolicy, listTopologyConfigs, type TopologyConfigItem } from '@/api/optimizations'

type GoalForm = {
  type: string
  weight: number
  sourceCity?: string
  topologyRef?: string
}

const router = useRouter()

const saving = ref(false)
const saveError = ref<string | null>(null)
const saveOk = ref<string | null>(null)

const namespace = ref('default')
const name = ref('')

const enabled = ref(true)
const runMode = ref<'Once' | 'Periodic'>('Once')
const rebalancePoint = ref('')
const strategy = ref<'Conservative' | 'Aggressive' | 'Preview'>('Conservative')
const thresholdPercent = ref<number>(0)

const matchLabels = ref<Array<{ key: string; value: string }>>([{ key: '', value: '' }])
const goals = ref<GoalForm[]>([])

const topologyOptions = ref<TopologyConfigItem[]>([])
const topologyLoading = ref(false)
const topologyError = ref<string | null>(null)

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

const totalWeight = computed(() => normalizeGoals().reduce((sum, g) => sum + (g.weight || 0), 0))

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

  const sum = gs.reduce((s, x) => s + (x.weight || 0), 0)
  if (sum <= 0) {
    const w = 1 / gs.length
    for (const g of goals.value) g.weight = w
    return
  }
  for (const g of goals.value) g.weight = (g.weight || 0) / sum
}

function buildPolicy() {
  const ns = namespace.value.trim() || 'default'
  const nm = name.value.trim()
  if (!nm) throw new Error('name is required')

  if (goals.value.length > 0 && Math.abs(totalWeight.value - 1) > 1e-6) {
    throw new Error(`goals weight sum must be 1 (current=${totalWeight.value.toFixed(6)})`)
  }

  const obj: any = {
    apiVersion: 'core.kubex.io/v1alpha1',
    kind: 'OptimizationPolicy',
    metadata: { name: nm, namespace: ns },
    spec: {
      enabled: enabled.value,
      runMode: runMode.value,
      strategy: strategy.value,
      targetSelector: { matchLabels: buildMatchLabels() },
      optimizationGoals: normalizeGoals().map((g) => {
        const out: any = { type: g.type, weight: g.weight }
        if (g.type === 'Latency' && g.sourceCity) out.sourceCity = g.sourceCity
        if (g.type === 'Communication' && g.topologyRef) out.topologyRef = g.topologyRef
        return out
      }),
    },
  }

  if (runMode.value === 'Periodic') {
    obj.spec.rebalancePoint = (rebalancePoint.value || '').trim()
  }

  if (strategy.value === 'Conservative') {
    obj.spec.improvementThresholdPercent = Math.max(0, Math.min(100, Number(thresholdPercent.value || 0)))
  }

  return obj
}

async function refreshTopology() {
  topologyLoading.value = true
  topologyError.value = null
  try {
    const resp = await listTopologyConfigs({ namespace: namespace.value })
    topologyOptions.value = (resp.items || []).filter((x) => !!x)
  } catch (e: any) {
    topologyError.value = e?.response?.data?.error || e?.message || String(e)
    topologyOptions.value = []
  } finally {
    topologyLoading.value = false
  }
}

async function onSave() {
  saving.value = true
  saveError.value = null
  saveOk.value = null
  try {
    const policy = buildPolicy()
    await createOptimizationPolicy({ policy })

    saveOk.value = 'created'
    // jump to detail page
    router.push(`/optimizations/policies/${encodeURIComponent(policy.metadata.namespace)}/${encodeURIComponent(policy.metadata.name)}`)
  } catch (e: any) {
    saveError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  refreshTopology()
})
</script>

<template>
  <section class="page">
    <div class="topbar">
      <div>
        <h1>Create OptimizationPolicy</h1>
        <p class="muted">Fill the form and save to create a policy.</p>
      </div>
      <div class="actions">
        <button class="btn" :disabled="saving" @click="refreshTopology">Refresh Topologies</button>
        <button class="btn primary" :disabled="saving" @click="onSave">Save</button>
      </div>
    </div>

    <p v-if="saveOk" class="ok">{{ saveOk }}</p>
    <p v-if="saveError" class="error">{{ saveError }}</p>

    <div class="card">
      <div class="pad">
        <h3 style="margin-top: 0">Metadata</h3>
        <div class="grid2">
          <label class="field">
            <span>Namespace</span>
            <input v-model="namespace" placeholder="default" />
          </label>
          <label class="field">
            <span>Name</span>
            <input v-model="name" placeholder="policy-001" />
          </label>
        </div>

        <h3>Basic</h3>
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

        <details style="margin-top: 14px">
          <summary class="muted">Preview generated YAML</summary>
          <pre class="mono" style="white-space: pre-wrap; margin-top: 10px">{{ dumpYaml(buildPolicy()) }}</pre>
        </details>
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
  border-color: #b91c1c;
  color: #b91c1c;
}
.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.error {
  color: #b91c1c;
  background: #fef2f2;
  border: 1px solid #fecaca;
  padding: 8px 10px;
  border-radius: 10px;
}
.ok {
  color: #065f46;
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
.goal-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
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
}
</style>
