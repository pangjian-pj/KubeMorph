<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { deleteClusters, importCluster, listClusters, type ClusterSummary } from '../api/clusters'

const { t } = useI18n()
const router = useRouter()

type Provider = 'AWS' | 'ACK' | 'TKE' | 'VKE' | 'GKE' | 'Azure' | 'Onprem'

const showImport = ref(false)
const submitting = ref(false)
const submitError = ref<string | null>(null)

const showDeleteConfirm = ref(false)
const deleting = ref(false)
const deleteError = ref<string | null>(null)
const deleteResult = ref<{ deleted: string[]; failed: Array<{ clusterId: string; message: string }> } | null>(null)

const selected = ref<Record<string, boolean>>({})

const loadingClusters = ref(false)
const clustersError = ref<string | null>(null)
const clusters = ref<ClusterSummary[]>([])
const total = ref(0)
const expandedLabels = ref<Record<string, boolean>>({})

const filters = reactive({
  name: '',
  provider: '',
  status: '',
  labelSelector: '',
})

const paging = reactive({
  page: 1,
  pageSize: 10,
})

function clusterKey(c: ClusterSummary) {
  return c.name || c.clusterId || JSON.stringify(c)
}

function isRowSelected(c: ClusterSummary) {
  const id = c.clusterId
  if (!id) return false
  return !!selected.value[id]
}

function toggleRowSelected(c: ClusterSummary, v: boolean) {
  const id = c.clusterId
  if (!id) return
  if (v) selected.value[id] = true
  else delete selected.value[id]
}

const selectedIds = computed(() => Object.keys(selected.value).filter((k) => selected.value[k]))
const selectedCount = computed(() => selectedIds.value.length)
const allSelectableIds = computed(() => clusters.value.map((c) => c.clusterId).filter((x): x is string => !!x))
const allSelectedOnPage = computed(() => {
  const ids = allSelectableIds.value
  return ids.length > 0 && ids.every((id) => !!selected.value[id])
})

function toggleSelectAllOnPage(v: boolean) {
  if (v) {
    for (const id of allSelectableIds.value) selected.value[id] = true
  } else {
    for (const id of allSelectableIds.value) delete selected.value[id]
  }
}

async function refreshClusters() {
  loadingClusters.value = true
  clustersError.value = null
  try {
    const resp = await listClusters({
      page: paging.page,
      pageSize: paging.pageSize,
      name: filters.name.trim() || undefined,
      provider: filters.provider || undefined,
      status: filters.status || undefined,
      labelSelector: filters.labelSelector.trim() || undefined,
    })
    clusters.value = resp.items || []
    total.value = resp.total || 0

    // 清理：列表刷新后，把当前页不存在的选择项移除
    const alive = new Set(clusters.value.map((c) => c.clusterId).filter((x): x is string => !!x))
    for (const id of Object.keys(selected.value)) {
      if (!alive.has(id)) delete selected.value[id]
    }
  } catch (e: any) {
    clustersError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    loadingClusters.value = false
  }
}

function formatCreatedAt(v?: string) {
  if (!v) return '-'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  return d.toLocaleString()
}

function labelEntries(labels?: Record<string, string>) {
  return Object.entries(labels || {}).sort((a, b) => a[0].localeCompare(b[0]))
}

function goDetail(c: ClusterSummary) {
  if (!c.clusterId) return
  router.push(`/clusters/${encodeURIComponent(c.clusterId)}`)
}

onMounted(() => {
  void refreshClusters()
})

const totalPages = computed(() => {
  const tp = Math.ceil((total.value || 0) / paging.pageSize)
  return tp <= 0 ? 1 : tp
})

async function onSearch() {
  paging.page = 1
  await refreshClusters()
}

async function onResetFilters() {
  filters.name = ''
  filters.provider = ''
  filters.status = ''
  filters.labelSelector = ''
  paging.page = 1
  await refreshClusters()
}

async function goPrev() {
  if (paging.page <= 1) return
  paging.page -= 1
  await refreshClusters()
}

async function goNext() {
  if (paging.page >= totalPages.value) return
  paging.page += 1
  await refreshClusters()
}

async function onChangePageSize() {
  paging.page = 1
  await refreshClusters()
}

const form = reactive({
  name: '',
  description: '',
  provider: 'Onprem' as Provider,
  kubeconfig: '',
  labelsText: '',
})

const canSubmit = computed(() => {
  return form.name.trim().length > 0 && form.kubeconfig.trim().length > 0
})

function resetForm() {
  form.name = ''
  form.description = ''
  form.provider = 'Onprem'
  form.kubeconfig = ''
  form.labelsText = ''
  submitError.value = null
}

function parseLabels(text: string): Record<string, string> | undefined {
  const t = text.trim()
  if (!t) return undefined

  // 支持两种格式：
  // 1) JSON：{"region":"cn-shanghai"}
  // 2) 每行 key=value
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

async function onSubmitImport() {
  if (!canSubmit.value) return
  submitting.value = true
  submitError.value = null
  try {
    await importCluster({
      name: form.name.trim(),
      description: form.description.trim() || undefined,
      provider: form.provider,
      kubeconfig: form.kubeconfig,
      labels: parseLabels(form.labelsText),
    })
    showImport.value = false
    resetForm()
  await refreshClusters()
  } catch (e: any) {
    submitError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    submitting.value = false
  }
}

function openDeleteConfirm(ids: string[]) {
  if (!ids.length) return
  deleteError.value = null
  deleteResult.value = null
  selected.value = ids.reduce<Record<string, boolean>>((acc, id) => {
    acc[id] = true
    return acc
  }, {})
  showDeleteConfirm.value = true
}

function openDeleteConfirmForRow(c: ClusterSummary) {
  if (!c.clusterId) return
  openDeleteConfirm([c.clusterId])
}

function openDeleteConfirmForSelected() {
  openDeleteConfirm(selectedIds.value)
}

async function onConfirmDelete() {
  const ids = selectedIds.value
  if (!ids.length) return
  deleting.value = true
  deleteError.value = null
  deleteResult.value = null
  try {
    const resp = await deleteClusters({ clusterIds: ids })
    deleteResult.value = { deleted: resp.deleted || [], failed: resp.failed || [] }
    await refreshClusters()
    if ((deleteResult.value.failed || []).length === 0) {
      showDeleteConfirm.value = false
      selected.value = {}
    }
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
        <h1>{{ t('clusters.title') }}</h1>
        <p>{{ t('clusters.desc') }}</p>
      </div>

      <div class="actions">
        <button class="btn" :disabled="loadingClusters" @click="refreshClusters">Refresh</button>
        <button class="btn primary" @click="showImport = true">{{ t('clusters.import.button') }}</button>
      </div>
    </div>

    <div class="card">
      <div class="filters-card">
        <div class="filters">
          <input
            v-model="filters.name"
            class="input"
            type="text"
            :placeholder="t('clusters.filters.searchPlaceholder')"
            @keyup.enter="onSearch"
          />

          <select v-model="filters.provider" class="input" @change="onSearch">
            <option value="">{{ t('clusters.filters.allProviders') }}</option>
            <option value="AWS">AWS</option>
            <option value="ACK">ACK</option>
            <option value="TKE">TKE</option>
            <option value="VKE">VKE</option>
            <option value="GKE">GKE</option>
            <option value="Azure">Azure</option>
            <option value="Onprem">Onprem</option>
          </select>

          <select v-model="filters.status" class="input" @change="onSearch">
            <option value="">{{ t('clusters.filters.allStatus') }}</option>
            <option value="Importing">Importing</option>
            <option value="Ready">Ready</option>
            <option value="NotReady">NotReady</option>
            <option value="Failed">Failed</option>
          </select>

          <input
            v-model="filters.labelSelector"
            class="input"
            type="text"
            :placeholder="t('clusters.filters.labelSelectorPlaceholder')"
            @keyup.enter="onSearch"
          />

          <span class="filters-spacer" />

          <button class="btn" @click="onSearch">{{ t('clusters.filters.search') }}</button>
          <button class="btn" @click="onResetFilters">{{ t('clusters.filters.reset') }}</button>

          <button class="btn danger" :disabled="selectedCount === 0 || loadingClusters" @click="openDeleteConfirmForSelected">
            {{ t('clusters.delete.batch') }} ({{ selectedCount }})
          </button>
        </div>
      </div>

      <div class="table">
        <div class="row header">
          <div class="check-col">
            <input
              type="checkbox"
              :checked="allSelectedOnPage"
              :disabled="!allSelectableIds.length"
              @change="toggleSelectAllOnPage(($event.target as HTMLInputElement).checked)"
            />
          </div>
          <div>{{ t('clusters.list.name') }}</div>
          <div>{{ t('clusters.list.description') }}</div>
          <div>{{ t('clusters.list.provider') }}</div>
          <div>{{ t('clusters.list.status') }}</div>
          <div>{{ t('clusters.list.labels') }}</div>
          <div>{{ t('clusters.list.createdAt') }}</div>
          <div>{{ t('clusters.list.actions') }}</div>
        </div>

        <div v-if="loadingClusters" class="row empty">
          <div class="muted">{{ t('clusters.list.loading') }}</div>
        </div>

        <div v-else-if="clustersError" class="row empty">
          <div class="error-inline">{{ clustersError }}</div>
        </div>

        <div v-else-if="!clusters.length" class="row empty">
          <div class="muted">{{ t('clusters.list.empty') }}</div>
        </div>

        <div v-else v-for="c in clusters" :key="clusterKey(c)" class="row">
          <div class="check-col">
            <input
              type="checkbox"
              :checked="isRowSelected(c)"
              :disabled="!c.clusterId"
              @change="toggleRowSelected(c, ($event.target as HTMLInputElement).checked)"
            />
          </div>
          <div class="name-cell">
            <span class="name">{{ c.name || '-' }}</span>
            <span v-if="c.clusterId" class="sub mono">{{ c.clusterId }}</span>
          </div>
          <div class="muted">{{ c.description || '-' }}</div>
          <div>{{ c.provider || '-' }}</div>
          <div>
            <span class="pill" :class="(c.status || '').toLowerCase()">{{ c.status || '-' }}</span>
          </div>
          <div>
            <div class="labels">
              <template v-if="labelEntries(c.labels).length">
                <span
                  v-for="([k, v], idx) in (expandedLabels[clusterKey(c)]
                    ? labelEntries(c.labels)
                    : labelEntries(c.labels).slice(0, 2))"
                  :key="k + '=' + v"
                  class="tag"
                >
                  {{ k }}={{ v }}
                </span>

                <button
                  v-if="labelEntries(c.labels).length > 2"
                  class="link-btn"
                  type="button"
                  @click="expandedLabels[clusterKey(c)] = !expandedLabels[clusterKey(c)]"
                >
                  <span v-if="expandedLabels[clusterKey(c)]">{{ t('clusters.list.collapse') }}</span>
                  <span v-else>{{ t('clusters.list.expand', { n: labelEntries(c.labels).length - 2 }) }}</span>
                </button>
              </template>
              <span v-else class="muted">-</span>
            </div>
          </div>
          <div class="muted">{{ formatCreatedAt(c.createdAt) }}</div>
          <div class="actions-col">
            <button class="btn small" :disabled="!c.clusterId" @click="goDetail(c)">
              {{ t('clusters.list.viewDetail') }}
            </button>
            <button class="btn small danger" :disabled="!c.clusterId" @click="openDeleteConfirmForRow(c)">
              {{ t('clusters.delete.single') }}
            </button>
          </div>
        </div>
      </div>

      <div class="footer">
        <div class="muted">{{ t('clusters.paging.total', { n: total }) }}</div>
        <div class="pager">
          <button class="btn" :disabled="paging.page <= 1 || loadingClusters" @click="goPrev">
            {{ t('clusters.paging.prev') }}
          </button>
          <span class="mono">{{ paging.page }} / {{ totalPages }}</span>
          <button class="btn" :disabled="paging.page >= totalPages || loadingClusters" @click="goNext">
            {{ t('clusters.paging.next') }}
          </button>
          <select v-model.number="paging.pageSize" class="input small" @change="onChangePageSize">
            <option :value="10">10</option>
            <option :value="20">20</option>
            <option :value="50">50</option>
          </select>
        </div>
      </div>
    </div>

    <div v-if="showImport" class="modal-backdrop" @click.self="showImport = false">
      <div class="modal">
        <div class="modal-header">
          <h2>{{ t('clusters.import.title') }}</h2>
          <button class="icon-btn" @click="showImport = false" aria-label="Close">×</button>
        </div>

        <div class="modal-body">
          <div class="grid">
            <label class="field">
              <span>{{ t('clusters.import.name') }}</span>
              <input v-model="form.name" type="text" :placeholder="t('clusters.import.namePlaceholder')" />
            </label>

            <label class="field">
              <span>{{ t('clusters.import.provider') }}</span>
              <select v-model="form.provider">
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
            <span>{{ t('clusters.import.description') }}</span>
            <input v-model="form.description" type="text" :placeholder="t('clusters.import.descriptionPlaceholder')" />
          </label>

          <label class="field">
            <span>{{ t('clusters.import.kubeconfig') }}</span>
            <textarea v-model="form.kubeconfig" rows="10" :placeholder="t('clusters.import.kubeconfigPlaceholder')" />
          </label>

          <label class="field">
            <span>{{ t('clusters.import.labels') }}</span>
            <textarea v-model="form.labelsText" rows="4" :placeholder="t('clusters.import.labelsPlaceholder')" />
            <small class="hint">{{ t('clusters.import.labelsHint') }}</small>
          </label>

          <p v-if="submitError" class="error">{{ submitError }}</p>
        </div>

        <div class="modal-footer">
          <button class="btn" @click="showImport = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" :disabled="!canSubmit || submitting" @click="onSubmitImport">
            <span v-if="submitting">{{ t('common.submitting') }}</span>
            <span v-else>{{ t('common.submit') }}</span>
          </button>
        </div>
      </div>
    </div>

    <div v-if="showDeleteConfirm" class="modal-backdrop" @click.self="!deleting && (showDeleteConfirm = false)">
      <div class="modal">
        <div class="modal-header">
          <h2>{{ t('clusters.delete.title') }}</h2>
          <button class="icon-btn" :disabled="deleting" @click="showDeleteConfirm = false" aria-label="Close">×</button>
        </div>

        <div class="modal-body">
          <p class="muted">{{ t('clusters.delete.confirm', { n: selectedCount }) }}</p>
          <ul class="mono small-list">
            <li v-for="id in selectedIds" :key="id">{{ id }}</li>
          </ul>

          <p v-if="deleteError" class="error">{{ deleteError }}</p>

          <div v-if="deleteResult" class="result">
            <div v-if="deleteResult.deleted.length" class="ok">
              {{ t('clusters.delete.deleted', { n: deleteResult.deleted.length }) }}
            </div>
            <div v-if="deleteResult.failed.length" class="error">
              {{ t('clusters.delete.failed', { n: deleteResult.failed.length }) }}
              <ul class="small-list">
                <li v-for="f in deleteResult.failed" :key="f.clusterId">
                  <span class="mono">{{ f.clusterId }}</span>: {{ f.message }}
                </li>
              </ul>
            </div>
          </div>
        </div>

        <div class="modal-footer">
          <button class="btn" :disabled="deleting" @click="showDeleteConfirm = false">{{ t('common.cancel') }}</button>
          <button class="btn danger" :disabled="deleting || selectedCount === 0" @click="onConfirmDelete">
            <span v-if="deleting">{{ t('common.submitting') }}</span>
            <span v-else>{{ t('clusters.delete.confirmButton') }}</span>
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
.btn.small {
  padding: 6px 10px;
  border-radius: 10px;
  font-size: 13px;
}
.btn.primary {
  background: #111827;
  border-color: #111827;
  color: #fff;
}

.btn.danger {
  background: #fff;
  border-color: #ef4444;
  color: #b91c1c;
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

.filters-card {
  background: #fff;
  padding: 12px;
  border-bottom: 1px solid #f3f4f6;
}
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.filters-spacer {
  flex: 1;
  min-width: 12px;
}

.input-wide {
  width: min(420px, 100%);
}

.filters-card {
  padding-bottom: 16px;
}
.pager {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.table {
  /* 给 sticky header 提供滚动容器 */
  max-height: 520px;
  overflow: auto;
  padding: 8px 0;
}
.input {
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  padding: 8px 10px;
  font-size: 14px;
  font-family: inherit;
  background: #fff;
}
.input.small {
  padding: 6px 8px;
  font-size: 13px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
}
.row {
  display: grid;
  grid-template-columns: 52px 1.2fr 1.4fr 0.8fr 0.8fr 1.6fr 1fr 0.9fr;
  gap: 12px;
  padding: 10px 12px;
  border-top: 1px solid #f3f4f6;
  align-items: start;
}

.check-col {
  display: flex;
  align-items: center;
  justify-content: center;
}

.actions-col {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.row.header {
  font-weight: 700;
  background: #f9fafb;
  border-top: none;
  position: sticky;
  top: 0;
  z-index: 1;
}
.row.empty {
  grid-template-columns: 1fr;
}
.muted {
  color: #6b7280;
}
.error-inline {
  color: #b91c1c;
}
.name-cell {
  display: grid;
  gap: 2px;
}
.name {
  font-weight: 700;
  color: #111827;
}
.sub {
  font-size: 12px;
  color: #9ca3af;
}
.pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  border: 1px solid;
  text-transform: none;
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
.pill.notready {
  background: #fef2f2;
  border-color: #fecaca;
  color: #991b1b;
}
.pill.failed {
  background: #fef2f2;
  border-color: #fecaca;
  color: #991b1b;
}

.labels {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px;
  border-top: 1px solid #f3f4f6;
  background: #fff;
}
.tag {
  font-size: 12px;
  border: 1px solid #e5e7eb;
  background: #f9fafb;
  color: #111827;
  padding: 2px 8px;
  border-radius: 999px;
}
.link-btn {
  border: none;
  background: transparent;
  color: #2563eb;
  padding: 0;
  cursor: pointer;
  font-size: 12px;
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,0.4);
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
.grid {
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
input, select, textarea {
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
.error {
  color: #b91c1c;
  background: #fef2f2;
  border: 1px solid #fecaca;
  padding: 8px 10px;
  border-radius: 10px;
}

.small-list {
  margin: 8px 0 0;
  padding-left: 18px;
  max-height: 220px;
  overflow: auto;
}

.result .ok {
  color: #065f46;
  background: #ecfdf5;
  border: 1px solid #6ee7b7;
  padding: 8px 10px;
  border-radius: 10px;
  margin-top: 10px;
}
</style>
