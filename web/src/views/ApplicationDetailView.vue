<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { getGlobalDeployment, type GetGlobalDeploymentResponse } from '@/api/applications'

const route = useRoute()
const router = useRouter()

const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<GetGlobalDeploymentResponse | null>(null)

const name = computed(() => String(route.params.name || ''))
const namespace = computed(() => String(route.params.namespace || 'default'))

const labelEntries = computed(() => Object.entries(data.value?.labels || {}))
const annotationEntries = computed(() => Object.entries(data.value?.annotations || {}))

async function load() {
	loading.value = true
	error.value = null
	try {
		data.value = await getGlobalDeployment({ name: name.value, namespace: namespace.value })
	} catch (e: any) {
		error.value = e?.response?.data?.error || e?.message || String(e)
		data.value = null
	} finally {
		loading.value = false
	}
}

onMounted(() => {
	load()
})

function back() {
	router.push('/applications')
}
</script>

<template>
	<section class="page">
		<div class="topbar">
			<div>
				<h1 class="mono">{{ namespace }}/{{ name }}</h1>
				<p class="muted">Application details</p>
			</div>
			<div class="actions">
				<button class="btn" :disabled="loading" @click="load">Refresh</button>
				<button class="btn" :disabled="loading" @click="back">Back</button>
			</div>
		</div>

		<p v-if="error" class="error" style="margin-top: 12px">{{ error }}</p>

		<div v-if="loading" class="card" style="margin-top: 12px">
			<div class="pad muted">Loading...</div>
		</div>

		<template v-else-if="data">
			<div class="card" style="margin-top: 12px">
				<div class="pad">
					<h2 style="margin: 0 0 10px">Base</h2>
					<div class="base-cards">
						<div class="base-card">
							<div class="base-k">Replicas</div>
							<div class="base-v mono">{{ data.replicas }}</div>
						</div>
						<div class="base-card">
							<div class="base-k">Running</div>
							<div class="base-v mono running">{{ data.runningReplicas }}</div>
						</div>
						<div class="base-card">
							<div class="base-k">Failed</div>
							<div class="base-v mono failed">{{ data.failedReplicas }}</div>
						</div>
						<div class="base-card">
							<div class="base-k">Phase</div>
							<div class="base-v"><span class="pill" :class="(data.phase || '').toLowerCase()">{{ data.phase }}</span></div>
						</div>
					</div>

					<div v-if="labelEntries.length" style="margin-top: 14px">
						<h3 style="margin: 0 0 6px">Labels</h3>
						<div class="chips">
							<span v-for="[k, v] in labelEntries" :key="k" class="chip mono">{{ k }}={{ v }}</span>
						</div>
					</div>

					<div v-if="annotationEntries.length" style="margin-top: 14px">
						<h3 style="margin: 0 0 6px">Annotations</h3>
						<div class="chips">
							<span v-for="[k, v] in annotationEntries" :key="k" class="chip mono">{{ k }}={{ v }}</span>
						</div>
					</div>
				</div>
			</div>

			<div class="card" style="margin-top: 12px">
				<div class="pad">
					<h2 style="margin: 0 0 10px">Bindings</h2>
					<div class="table-wrap">
						<table class="table">
							<thead>
								<tr>
									<th class="num">ReplicaId</th>
									<th>Cluster</th>
									<th>Node</th>
									<th>Phase</th>
									<th>LastTransitionTime</th>
									<th>LastError</th>
								</tr>
							</thead>
							<tbody>
								<tr v-if="(data.bindings || []).length === 0">
									<td colspan="6" class="muted">No bindings</td>
								</tr>
								<tr v-else v-for="b in data.bindings" :key="b.replicaIndex">
									<td class="num mono">{{ b.replicaIndex }}</td>
									<td class="mono">{{ b.clusterName || b.clusterId || '-' }}</td>
									<td class="mono">{{ b.nodeName || '-' }}</td>
									<td><span class="pill" :class="(b.phase || '').toLowerCase()">{{ b.phase || '-' }}</span></td>
									<td class="mono">{{ (b.lastTransitionTime || '').replace('T', ' ').replace('Z', '') }}</td>
									<td class="mono">{{ b.lastError || '' }}</td>
								</tr>
							</tbody>
						</table>
					</div>
				</div>
			</div>

			<div class="card" style="margin-top: 12px">
				<div class="pad">
					<h2 style="margin: 0 0 10px">Template</h2>
					<pre class="pre">{{ JSON.stringify(data.template ?? {}, null, 2) }}</pre>
				</div>
			</div>
		</template>
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
.btn:disabled {
	opacity: 0.6;
	cursor: not-allowed;
}
.base-cards {
	display: grid;
	grid-template-columns: repeat(4, minmax(0, 1fr));
	gap: 12px;
}
.base-card {
	border: 1px solid #e5e7eb;
	border-radius: 12px;
	padding: 10px 12px;
	background: #fff;
}
.base-k {
	color: #6b7280;
	font-size: 12px;
}
.base-v {
	margin-top: 6px;
	font-size: 18px;
	font-weight: 600;
	color: #111827;
}
.chips {
	display: flex;
	flex-wrap: wrap;
	gap: 8px;
}
.chip {
	padding: 4px 8px;
	border-radius: 999px;
	background: #f3f4f6;
	border: 1px solid #e5e7eb;
	font-size: 12px;
}
.table-wrap {
	overflow: auto;
	border: 1px solid #e5e7eb;
	border-radius: 12px;
}
.table {
	width: 100%;
	border-collapse: collapse;
	min-width: 820px;
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
}
.num {
	text-align: right;
}
.pre {
	background: #0b1020;
	color: #e5e7eb;
	padding: 12px;
	border-radius: 12px;
	overflow: auto;
	font-size: 12px;
	line-height: 1.4;
}
.mono {
	font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
}
.pill {
	display: inline-flex;
	align-items: center;
	padding: 2px 10px;
	border-radius: 999px;
	border: 1px solid #e5e7eb;
	font-size: 12px;
	color: #111827;
	background: #fff;
}
.pill.running {
	border-color: #86efac;
	background: #dcfce7;
}
.pill.failed,
.pill.degraded {
	border-color: #fecaca;
	background: #fee2e2;
}
.pill.pending,
.pill.progressing,
.pill.applying,
.pill.assigned {
	border-color: #c7d2fe;
	background: #eef2ff;
}
.running {
	color: #16a34a;
}
.failed {
	color: #dc2626;
}
</style>
