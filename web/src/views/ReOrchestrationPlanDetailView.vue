<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { dump as dumpYaml } from 'js-yaml'
import { use } from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'
import { GraphChart } from 'echarts/charts'
import { TooltipComponent, LegendComponent } from 'echarts/components'
import VChart from 'vue-echarts'

import { getReOrchestrationPlan } from '@/api/optimizations'

use([SVGRenderer, GraphChart, TooltipComponent, LegendComponent])

const route = useRoute()
const router = useRouter()
const namespace = computed(() => String(route.params.namespace || 'default'))
const name = computed(() => String(route.params.name || ''))

const loading = ref(false)
const error = ref<string | null>(null)
const plan = ref<any>(null)
const yamlText = ref('')

async function refresh() {
  if (!name.value) return
  loading.value = true
  error.value = null
  try {
    const resp = await getReOrchestrationPlan({ namespace: namespace.value, name: name.value })
    plan.value = resp.plan
    yamlText.value = dumpYaml(resp.plan)
  } catch (e: any) {
    error.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    loading.value = false
  }
}

const moves = computed(() => (plan.value?.spec?.moves || []) as any[])
const summary = computed(() => plan.value?.spec?.summary || {})
const goalScores = computed(() => (summary.value?.goalScores || {}) as Record<string, any>)
const goalScoreRows = computed(() => {
  const m = goalScores.value || {}
  return Object.keys(m)
    .sort()
    .map((k) => ({
      goal: k,
      weight: m[k]?.weight,
      currentScore: m[k]?.currentScore,
      expectedScore: m[k]?.expectedScore,
      improvement: m[k]?.estimatedImprovementScore,
      human: m[k]?.humanReadable,
    }))
})
const statusMoveStatuses = computed(() => (plan.value?.status?.moveStatuses || []) as any[])

const createdAt = computed(() => {
  const ts = plan.value?.metadata?.creationTimestamp
  return typeof ts === 'string' ? ts.replace('T', ' ').replace('Z', '') : ''
})

const statusByKey = computed(() => {
  const m = new Map<string, any>()
  for (const s of statusMoveStatuses.value) {
    const gdns = String(s?.globalDeploymentRef?.namespace || '')
    const gdnm = String(s?.globalDeploymentRef?.name || '')
    const idx = String(s?.replicaIndex ?? '')
    if (!gdnm || idx === '') continue
    m.set(`${gdns}/${gdnm}#${idx}`, s)
  }
  return m
})

function statusKeyOfMove(mv: any) {
  const gdns = String(mv?.globalDeploymentRef?.namespace || '')
  const gdnm = String(mv?.globalDeploymentRef?.name || '')
  const idx = String(mv?.replicaIndex ?? '')
  if (!gdnm || idx === '') return ''
  return `${gdns}/${gdnm}#${idx}`
}

function moveStatusOfMove(mv: any) {
  const key = statusKeyOfMove(mv)
  if (!key) return { status: 'Pending', message: '' }
  const st = statusByKey.value.get(key)
  return {
    status: String(st?.status || 'Pending'),
    message: String(st?.message || ''),
  }
}

function moveEdgeColor(status: string) {
  const v = (status || '').toLowerCase()
  if (v === 'succeeded') return '#10b981'
  if (v === 'failed') return '#ef4444'
  if (v === 'inprogress' || v === 'in_progress') return '#3b82f6'
  return '#9ca3af'
}

const moveRows = computed(() => {
  return moves.value.map((mv) => {
    const gdns = String(mv?.globalDeploymentRef?.namespace || '')
    const gdnm = String(mv?.globalDeploymentRef?.name || '')
    const idx = String(mv?.replicaIndex ?? '')
    const st = statusByKey.value.get(`${gdns}/${gdnm}#${idx}`)
    return {
      mv,
      status: String(st?.status || 'Pending'),
      message: String(st?.message || ''),
      // duration currently not in CRD; keep placeholder for future
      duration: String(st?.duration || st?.elapsed || ''),
    }
  })
})

const activeTab = ref<'moves' | 'yaml'>('moves')
const movesView = ref<'list' | 'graph'>('graph')

function fmtNum(n: any) {
  const v = typeof n === 'number' ? n : Number(n)
  if (!Number.isFinite(v)) return '-'
  return v.toFixed(1)
}

function fmtPct(n: any) {
  const v = typeof n === 'number' ? n : Number(n)
  if (!Number.isFinite(v)) return '-'
  return `${v.toFixed(1)}%`
}

function fmtVal(n: any, digits = 2) {
  const v = typeof n === 'number' ? n : Number(n)
  if (!Number.isFinite(v)) return '-'
  return v.toFixed(digits)
}

function humanImprovementPct(h: any) {
  if (!h) return null
  const from = typeof h?.from === 'number' ? h.from : Number(h?.from)
  const to = typeof h?.to === 'number' ? h.to : Number(h?.to)
  if (!Number.isFinite(from) || !Number.isFinite(to)) return null
  // Improvement is defined as relative decrease (lower is better) in original units.
  // pct = (from - to) / max(|from|, eps) * 100
  const eps = 1e-9
  const den = Math.max(Math.abs(from), eps)
  return ((from - to) / den) * 100.0
}

function humanLine(h: any) {
  if (!h || (h.from == null && h.to == null)) return ''
  const unit = String(h?.unit || '')
  const kind = String(h?.kind || '')
  const from = h?.from
  const to = h?.to
  if (from == null || to == null) {
    return unit ? `${kind}: ${from ?? '-'} → ${to ?? '-'} ${unit}` : `${kind}: ${from ?? '-'} → ${to ?? '-'}`
  }
  const suffix = unit ? ` ${unit}` : ''
  return `${fmtVal(from)}${suffix} → ${fmtVal(to)}${suffix}`
}

function moveStatusClass(s: string) {
  const v = (s || '').toLowerCase()
  if (v === 'succeeded') return 'succeeded'
  if (v === 'inprogress' || v === 'in_progress') return 'inprogress'
  if (v === 'failed') return 'failed'
  return 'pending'
}

function moveStatusLabel(s: string) {
  const v = (s || '').trim()
  return v || 'Pending'
}

function locClusterLabel(loc: any) {
  return String(loc?.clusterName || loc?.clusterId || loc?.clusterID || '-')
}

function locNodeLabel(loc: any) {
  const cluster = locClusterLabel(loc)
  const node = String(loc?.nodeName || '-')
  return `${cluster} / ${node}`
}

function scoreStatus(cur: any, exp: any) {
  const c = typeof cur === 'number' ? cur : Number(cur)
  const e = typeof exp === 'number' ? exp : Number(exp)
  if (!Number.isFinite(c) || !Number.isFinite(e)) return 'na'
  if (e < c) return 'improve'
  if (e > c) return 'worse'
  return 'same'
}

function stableColorFromString(s: string) {
  // deterministic soft color from string hash
  let h = 0
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0
  const hue = h % 360
  return `hsl(${hue} 70% 45%)`
}

type GraphNode = {
  id: string
  name: string
  category: number
  symbolSize?: number
  itemStyle?: any
  label?: any
  value?: any
}

type GraphLink = {
  source: string
  target: string
  value?: any
  lineStyle?: any
  emphasis?: any
}

const graphData = computed(() => {
  const nodeById = new Map<string, GraphNode>()
  const links: GraphLink[] = []

  function ensureNode(id: string, name: string, category: number) {
    const existed = nodeById.get(id)
    if (existed) return existed

    const n: GraphNode = {
      id,
      name,
      category,
      symbolSize: category === 0 ? 52 : 34,
      itemStyle: {
        color: category === 0 ? stableColorFromString(name) : '#0f172a',
        borderColor: category === 0 ? 'rgba(255,255,255,0.85)' : 'rgba(255,255,255,0.95)',
        borderWidth: 2,
        shadowBlur: 14,
        shadowColor: 'rgba(2,6,23,0.16)',
      },
      label: {
        show: true,
        color: category === 0 ? '#0f172a' : '#111827',
        fontWeight: 800,
        backgroundColor: 'rgba(255,255,255,0.85)',
        borderRadius: 6,
        padding: [3, 6],
      },
    }
    nodeById.set(id, n)
    return n
  }

  for (const mv of moves.value) {
    const srcCluster = locClusterLabel(mv?.source)
    const dstCluster = locClusterLabel(mv?.destination)
    const srcNodeName = String(mv?.source?.nodeName || '-')
    const dstNodeName = String(mv?.destination?.nodeName || '-')

    const srcClusterId = `c:${srcCluster}`
    const dstClusterId = `c:${dstCluster}`
    ensureNode(srcClusterId, srcCluster, 0)
    ensureNode(dstClusterId, dstCluster, 0)

    const srcNodeId = `n:${srcCluster}/${srcNodeName}`
    const dstNodeId = `n:${dstCluster}/${dstNodeName}`
    ensureNode(srcNodeId, `${srcNodeName}`, 1)
    ensureNode(dstNodeId, `${dstNodeName}`, 1)

    // cluster-to-node edges (light)
    links.push({
      source: srcClusterId,
      target: srcNodeId,
      lineStyle: { color: 'rgba(17,24,39,0.18)', width: 1 },
      emphasis: { focus: 'adjacency' },
    })
    links.push({
      source: dstClusterId,
      target: dstNodeId,
      lineStyle: { color: 'rgba(17,24,39,0.18)', width: 1 },
      emphasis: { focus: 'adjacency' },
    })

  const gdname = String(mv?.globalDeploymentRef?.name || '')
  const idx = String(mv?.replicaIndex ?? '')
  const tip = gdname && idx !== '' ? `${gdname}#${idx}` : 'move'

  const st = moveStatusOfMove(mv)
  const edgeColor = moveEdgeColor(st.status)

    // move edge (bold)
    links.push({
      source: srcNodeId,
      target: dstNodeId,
      value: {
        title: tip,
        status: st.status,
        message: st.message,
      },
      lineStyle: { width: 3.0, color: edgeColor, opacity: 0.85, curveness: 0.18 },
      emphasis: { focus: 'adjacency' },
    })
  }

  return {
    nodes: Array.from(nodeById.values()),
    links,
    categories: [{ name: 'Cluster' }, { name: 'Node' }],
  }
})

const graphStats = computed(() => {
  const { nodes, links } = graphData.value
  const clusterCount = nodes.filter((n) => n.category === 0).length
  const nodeCount = nodes.filter((n) => n.category === 1).length
  const moveCount = links.filter((l) => l?.value != null).length
  return { clusterCount, nodeCount, moveCount }
})

const graphOption = computed(() => {
  const { nodes, links, categories } = graphData.value
  return {
    tooltip: {
      trigger: 'item',
      confine: true,
      formatter: (p: any) => {
        if (p?.dataType === 'edge') {
          const v = p?.data?.value
          if (v && typeof v === 'object') {
            const title = String(v?.title || 'move')
            const st = String(v?.status || 'Pending')
            const msg = String(v?.message || '')
            const msgHtml = msg ? `<div style="margin-top:6px;color:#334155;white-space:pre-wrap">${msg}</div>` : ''
            return `<div style="font-weight:800;color:#0f172a">${title}</div><div style="margin-top:4px">Status: <b>${st}</b></div>${msgHtml}`
          }
          return v ? String(v) : 'move'
        }
        if (p?.dataType === 'node') {
          const nm = String(p?.data?.name || '')
          const cat = Number(p?.data?.category)
          return cat === 0 ? `Cluster: ${nm}` : `Node: ${nm}`
        }
        return ''
      },
    },
    legend: [
      {
        data: categories.map((c: any) => c.name),
        top: 10,
        left: 12,
        itemWidth: 12,
        itemHeight: 12,
        textStyle: { color: '#334155', fontWeight: 700 },
      },
    ],
    series: [
      {
        type: 'graph',
        layout: 'force',
        roam: true,
        draggable: true,
        categories,
        data: nodes,
        links,
        edgeSymbol: ['none', 'arrow'],
        edgeSymbolSize: 11,
        force: {
          repulsion: 360,
          edgeLength: [80, 210],
          gravity: 0.06,
        },
        label: { position: 'right' },
        lineStyle: { curveness: 0.14, opacity: 0.55 },
        emphasis: { scale: true, lineStyle: { opacity: 0.9 } },
      },
    ],
  }
})


function toBack() {
  router.back()
}

function onEdit() {
  // readonly page for v1; keep button for future extension
}

onMounted(() => refresh())
</script>

<template>
  <section class="page">
    <div class="topbar">
      <div>
        <h1 class="mono">{{ namespace }}/{{ name }}</h1>
        <p class="muted">ReOrchestrationPlan</p>
      </div>
      <div class="actions">
        <button class="btn" :disabled="loading" @click="toBack">Back</button>
        <button class="btn" :disabled="loading" @click="refresh">Refresh</button>
        <button class="btn" :disabled="true" @click="onEdit">Edit</button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="card summary-card" style="margin-top: 14px">
      <div class="pad">
        <div class="summary-title">Summary</div>
        <p class="summary-line" v-if="summary.currentScore != null && summary.expectedScore != null && summary.estimatedImprovementScore != null">
          This optimization is expected to reduce the total score from <b class="mono">{{ fmtNum(summary.currentScore) }}</b> to <b class="mono">{{ fmtNum(summary.expectedScore) }}</b>, achieving an improvement of <b>{{ fmtPct(summary.estimatedImprovementScore) }}</b>.
        </p>
        <p class="summary-line" v-else>
          The summary for this optimization is incomplete.
        </p>
        <p class="summary-line">Planned to move <b class="mono">{{ summary.podsToMove ?? moves.length ?? 0 }}</b> replica(s).</p>
        <p class="summary-line">Generated by policy <b class="mono">{{ summary.policyName ?? '-' }}</b> at <b class="mono">{{ createdAt || '-' }}</b>.</p>

        <div class="summary-kv" style="margin-top: 10px">
          <div><span class="k">Policy</span><span class="v mono">{{ summary.policyName ?? '-' }}</span></div>
          <div><span class="k">PodsToMove</span><span class="v mono">{{ summary.podsToMove ?? '-' }}</span></div>
          <div><span class="k">CurrentScore</span><span class="v mono">{{ summary.currentScore ?? '-' }}</span></div>
          <div><span class="k">ExpectedScore</span><span class="v mono">{{ summary.expectedScore ?? '-' }}</span></div>
          <div><span class="k">Improvement</span><span class="v mono">{{ summary.estimatedImprovementScore ?? '-' }}</span></div>
        </div>

        <div v-if="goalScoreRows.length > 0" style="margin-top: 12px">
          <div class="summary-title" style="font-size: 14px; color: #1f2937">Optimization results by goal</div>

          <div class="table-wrap" style="margin-top: 10px; background: #fff">
            <table class="table" style="min-width: 760px">
              <thead>
                <tr>
                  <th>Objective</th>
                  <th>Weight</th>
                  <th>Before → After</th>
                  <th>Change</th>
                  <th>Improvement (%)</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(r, i) in goalScoreRows" :key="i">
                  <td class="mono">
                    {{ r.goal }}
                  </td>
                  <td class="mono">{{ r.weight ?? '-' }}</td>
                  <td>
                    <div v-if="r.human" class="mono" style="font-weight: 800; color: #0f172a">
                      {{ humanLine(r.human) }}
                    </div>
                    <div v-else class="muted">-</div>
                    <div v-if="r.human?.detail?.sourceCity" class="muted" style="margin-top: 4px">
                      sourceCity: <span class="mono">{{ r.human.detail.sourceCity }}</span>
                    </div>
                  </td>
                  <td class="mono">
                    <span v-if="r.human?.delta != null" class="pill" :class="(Number(r.human.delta) >= 0 ? 'improve' : 'worse')">
                      {{ fmtVal(r.human.delta) }}
                    </span>
                    <span v-else class="muted">-</span>
                  </td>
                  <td class="mono">
                    <span v-if="humanImprovementPct(r.human) != null" class="pill improve">
                      {{ fmtPct(humanImprovementPct(r.human)) }}
                    </span>
                    <span v-else-if="r.improvement != null" class="pill improve" title="Fallback: normalized score improvement">
                      {{ fmtPct(r.improvement) }}
                    </span>
                    <span v-else class="muted">-</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 14px">
      <div class="pad">
        <div class="tabs">
          <button class="tab" :class="{ active: activeTab === 'moves' }" @click="activeTab = 'moves'">Moves List</button>
          <button class="tab" :class="{ active: activeTab === 'yaml' }" @click="activeTab = 'yaml'">Raw Data</button>
        </div>

        <div v-if="activeTab === 'moves'">
          <h3 style="margin-top: 12px">Moves List</h3>

          <div class="subtabs" style="margin-top: 10px">
            <button class="subtab" :class="{ active: movesView === 'graph' }" @click="movesView = 'graph'">Graph</button>
            <button class="subtab" :class="{ active: movesView === 'list' }" @click="movesView = 'list'">Table</button>
          </div>

          <div v-if="movesView === 'graph'" style="margin-top: 12px">
            <div class="graph-wrap">
              <div v-if="loading" class="muted pad">Loading...</div>
              <div v-else-if="moveRows.length === 0" class="muted pad">No moves</div>
              <VChart v-else class="rop-graph" :option="graphOption" autoresize />
            </div>

            <div class="graph-hint">
              <div class="muted">Tip: drag nodes / use the mouse wheel to zoom / pan the canvas; hover an edge to see which replica move it represents.</div>
              <div class="mono muted">Clusters: {{ graphStats.clusterCount }} · Nodes: {{ graphStats.nodeCount }} · Moves: {{ graphStats.moveCount }}</div>
            </div>
          </div>

          <div class="table-wrap" style="margin-top: 10px">
            <table v-if="movesView === 'list'" class="table">
              <thead>
                <tr>
                  <th>Status</th>
                  <th>Replica</th>
                  <th>Migration Path</th>
                  <th>Message</th>
                  <th>Duration</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading">
                  <td colspan="5" class="muted">Loading...</td>
                </tr>
                <tr v-else-if="moveRows.length === 0">
                  <td colspan="5" class="muted">No moves</td>
                </tr>
                <tr v-else v-for="(row, idx) in moveRows" :key="idx">
                  <td>
                    <span class="mstatus" :class="moveStatusClass(row.status)">
                      <span class="dot" />
                      {{ moveStatusLabel(row.status) }}
                    </span>
                  </td>
                  <td class="mono">
                    {{ row.mv?.globalDeploymentRef?.name }}#{{ row.mv?.replicaIndex }}
                  </td>
                  <td class="mono">
                    {{ locNodeLabel(row.mv?.source) }}
                    →
                    {{ locNodeLabel(row.mv?.destination) }}
                  </td>
                  <td class="message">{{ row.message || '-' }}</td>
                  <td class="mono">{{ row.duration || '-' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div v-else>
          <h3 style="margin-top: 12px">Raw Data</h3>
          <textarea v-model="yamlText" rows="18" class="mono" style="width: 100%" readonly />
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

.summary-card {
  border-color: #dbeafe;
  background: #eff6ff;
}
.summary-title {
  font-weight: 800;
  color: #1d4ed8;
}
.summary-line {
  margin: 8px 0 0;
  color: #111827;
}
.summary-kv {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 14px;
}
.summary-kv > div {
  display: flex;
  justify-content: space-between;
  border-top: 1px solid rgba(191, 219, 254, 0.9);
  padding: 6px 0;
}
.summary-kv > div:nth-child(1),
.summary-kv > div:nth-child(2) {
  border-top: none;
}
.k {
  color: #1f2937;
  opacity: 0.75;
}
.v {
  color: #111827;
}

.tabs {
  display: flex;
  gap: 10px;
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

.subtabs {
  display: flex;
  gap: 10px;
}
.subtab {
  border: 1px solid #e5e7eb;
  background: #fff;
  color: #111827;
  padding: 6px 10px;
  border-radius: 10px;
  cursor: pointer;
  font-weight: 700;
  font-size: 12px;
}
.subtab.active {
  background: #2563eb;
  border-color: #2563eb;
  color: #fff;
}

.graph-wrap {
  border: 1px solid #e5e7eb;
  border-radius: 12px;
  overflow: hidden;
  background: #fff;
  box-shadow: 0 12px 40px rgba(2, 6, 23, 0.06);
}
.rop-graph {
  width: 100%;
  height: 460px;
}

.graph-hint {
  margin: 8px 2px 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.table-wrap {
  overflow: auto;
  border: 1px solid #e5e7eb;
  border-radius: 12px;
}
.table {
  width: 100%;
  border-collapse: collapse;
  min-width: 980px;
}
.table th,
.table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f3f4f6;
  text-align: left;
  font-size: 14px;
  vertical-align: top;
}
.table th {
  font-size: 12px;
  color: #6b7280;
  font-weight: 800;
  background: #fafafa;
}
.message {
  max-width: 520px;
  white-space: pre-wrap;
  word-break: break-word;
}

.mstatus {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-weight: 800;
  font-size: 12px;
}
.dot {
  width: 10px;
  height: 10px;
  border-radius: 999px;
  border: 2px solid;
}
.mstatus.succeeded .dot {
  background: #10b981;
  border-color: #10b981;
}
.mstatus.inprogress .dot {
  background: #3b82f6;
  border-color: #3b82f6;
}
.mstatus.pending .dot {
  background: transparent;
  border-color: #9ca3af;
}
.mstatus.failed .dot {
  background: #ef4444;
  border-color: #ef4444;
}

.pill {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 2px 8px;
  border-radius: 999px;
  font-weight: 900;
  font-size: 12px;
  border: 1px solid rgba(148, 163, 184, 0.55);
  background: rgba(148, 163, 184, 0.08);
}
.pill.improve {
  border-color: rgba(16, 185, 129, 0.55);
  background: rgba(16, 185, 129, 0.12);
  color: #047857;
}
.pill.worse {
  border-color: rgba(239, 68, 68, 0.55);
  background: rgba(239, 68, 68, 0.10);
  color: #b91c1c;
}
.pill.same {
  border-color: rgba(59, 130, 246, 0.45);
  background: rgba(59, 130, 246, 0.10);
  color: #1d4ed8;
}
.pill.na {
  opacity: 0.75;
}

@media (max-width: 900px) {
  .summary-kv {
    grid-template-columns: 1fr;
  }
}
</style>
