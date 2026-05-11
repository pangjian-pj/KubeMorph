<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { load as loadYaml } from 'js-yaml'
import { useRouter } from 'vue-router'

import { createGlobalDeployment, deleteGlobalDeployment, listGlobalDeployments, type GlobalDeploymentItem } from '@/api/applications'

const { t } = useI18n()
const router = useRouter()

const showCreate = ref(false)
const creating = ref(false)
const createError = ref<string | null>(null)
const createOk = ref<string | null>(null)

const showDeleteConfirm = ref(false)
const deleting = ref(false)
const deleteError = ref<string | null>(null)
const deleteTarget = ref<{ name: string; namespace: string } | null>(null)

const selected = ref<Record<string, boolean>>({})
const showBatchDeleteConfirm = ref(false)
const batchDeleteError = ref<string | null>(null)
const batchDeleteResult = ref<{ total: number; deleted: number; failed: number; failedItems: Array<{ name: string; namespace: string; error: string }> } | null>(null)

const loading = ref(false)
const loadError = ref<string | null>(null)
const items = ref<GlobalDeploymentItem[]>([])
const total = ref(0)

const filters = ref({
  name: '',
  namespace: 'default',
})

const page = ref(1)
const pageSize = ref(10)

const totalPages = computed(() => {
  return Math.max(1, Math.ceil((total.value || 0) / pageSize.value))
})

const visibleKeys = computed(() => {
  return (items.value || []).map((it) => `${it.namespace}/${it.name}`)
})

const selectedKeys = computed(() => {
  return Object.keys(selected.value).filter((k) => selected.value[k])
})

const selectedCount = computed(() => selectedKeys.value.length)

const isAllVisibleSelected = computed(() => {
  if (!visibleKeys.value.length) return false
  return visibleKeys.value.every((k) => selected.value[k])
})

const isSomeVisibleSelected = computed(() => {
  if (!visibleKeys.value.length) return false
  return visibleKeys.value.some((k) => selected.value[k]) && !isAllVisibleSelected.value
})

function normalizeSelection() {
  const valid = new Set(visibleKeys.value)
  const next: Record<string, boolean> = {}
  // keep previously selected items even if they are off the current page
  for (const k of Object.keys(selected.value)) next[k] = selected.value[k]
  // drop explicit false values to keep the map small
  for (const k of Object.keys(next)) {
    if (!next[k]) delete next[k]
  }
  // ensure current page keys exist when selected
  for (const k of valid) {
    if (next[k]) next[k] = true
  }
  selected.value = next
}

async function refreshList() {
  loading.value = true
  loadError.value = null
  try {
    const resp = await listGlobalDeployments({
      name: filters.value.name.trim() || undefined,
      namespace: filters.value.namespace.trim() || undefined,
      page: page.value,
      pageSize: pageSize.value,
    })
    items.value = resp.items || []
    total.value = resp.total || 0
    // 防止删除/筛选后当前页越界
    if (page.value > totalPages.value) page.value = totalPages.value
  } catch (e: any) {
    loadError.value = e?.response?.data?.error || e?.message || String(e)
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  normalizeSelection()
  }
}

function onSearch() {
  page.value = 1
  refreshList()
}

function toPrev() {
  if (page.value > 1) page.value--
}
function toNext() {
  if (page.value < totalPages.value) page.value++
}

watch([page, pageSize], () => {
  refreshList()
})

onMounted(() => {
  refreshList()
})

const form = ref({
  yamlText: '',
})

function openCreate() {
  createOk.value = null
  createError.value = null
  form.value = {
  yamlText: '',
  }
  showCreate.value = true
}

function validateYaml(text: string) {
  const s = (text || '').trim()
  if (!s) return null
  // 只做校验：能 parse 说明 YAML 语法 OK。
  // 具体字段结构由后端验证。
  loadYaml(s)
  return s
}

async function onSubmitCreate() {
  createOk.value = null
  createError.value = null
  creating.value = true
  try {
  const yamlText = validateYaml(form.value.yamlText)
  if (!yamlText) throw new Error('yaml is required')

  const resp = await createGlobalDeployment({ yaml: yamlText })
    createOk.value = resp.message || 'created'
    showCreate.value = false
    // 创建成功后刷新列表
    page.value = 1
    await refreshList()
  } catch (e: any) {
    createError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    creating.value = false
  }
}

function toDetails(it: GlobalDeploymentItem) {
  router.push(`/applications/${encodeURIComponent(it.namespace)}/${encodeURIComponent(it.name)}`)
}

function openDeleteConfirm(it: GlobalDeploymentItem) {
  deleteError.value = null
  deleteTarget.value = { name: it.name, namespace: it.namespace }
  showDeleteConfirm.value = true
}

function toggleAllVisible() {
  const next = !isAllVisibleSelected.value
  const m: Record<string, boolean> = { ...selected.value }
  for (const k of visibleKeys.value) {
    if (next) m[k] = true
    else delete m[k]
  }
  selected.value = m
}

function openBatchDeleteConfirm() {
  batchDeleteError.value = null
  batchDeleteResult.value = null
  showBatchDeleteConfirm.value = true
}

async function onConfirmDelete() {
  if (!deleteTarget.value) return
  deleting.value = true
  deleteError.value = null
  try {
    await deleteGlobalDeployment({ name: deleteTarget.value.name, namespace: deleteTarget.value.namespace })
  const deletedKey = `${deleteTarget.value.namespace}/${deleteTarget.value.name}`
    showDeleteConfirm.value = false
    deleteTarget.value = null
    // remove from selection if present
  if (deletedKey && selected.value[deletedKey]) {
      const m = { ...selected.value }
    delete m[deletedKey]
      selected.value = m
    }
    // 删除后刷新列表（保持当前 page，但防止越界由 refreshList 处理）
    await refreshList()
  } catch (e: any) {
    deleteError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    deleting.value = false
  }
}

async function onConfirmBatchDelete() {
  const keys = selectedKeys.value
  if (!keys.length) {
    showBatchDeleteConfirm.value = false
    return
  }
  deleting.value = true
  batchDeleteError.value = null
  batchDeleteResult.value = null

  const failures: Array<{ name: string; namespace: string; error: string }> = []
  let deleted = 0
  try {
    for (const k of keys) {
      const [namespace, name] = k.split('/')
      try {
        await deleteGlobalDeployment({ name, namespace })
        deleted++
      } catch (e: any) {
        failures.push({
          name,
          namespace,
          error: e?.response?.data?.error || e?.message || String(e),
        })
      }
    }

    batchDeleteResult.value = {
      total: keys.length,
      deleted,
      failed: failures.length,
      failedItems: failures,
    }

    // clear selection for successfully deleted items
    const failedSet = new Set(failures.map((f) => `${f.namespace}/${f.name}`))
    const next: Record<string, boolean> = {}
    for (const k of keys) {
      if (failedSet.has(k)) next[k] = true
    }
    // keep any other selections outside this batch
    for (const k of Object.keys(selected.value)) {
      if (!keys.includes(k) && selected.value[k]) next[k] = true
    }
    selected.value = next

    // refresh list after deletion
    await refreshList()
  } catch (e: any) {
    batchDeleteError.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <section class="page">
    <div class="topbar">
      <div>
        <h1>{{ t('applications.title') }}</h1>
        <p class="muted">{{ t('applications.desc') }}</p>
      </div>
      <div class="actions">
	<button class="btn" :disabled="loading" @click="refreshList">Refresh</button>
        <button class="btn danger" :disabled="loading || deleting || selectedCount === 0" @click="openBatchDeleteConfirm">
          Delete Selected ({{ selectedCount }})
        </button>
        <button class="btn primary" @click="openCreate">{{ t('applications.create.button') }}</button>
      </div>
    </div>

    <div v-if="createOk" class="card">
      <div class="pad ok">{{ createOk }}</div>
    </div>

    <div class="card">
      <div class="pad">
        <div class="filters">
          <label class="field" style="margin: 0">
            <span>Namespace</span>
            <input v-model="filters.namespace" type="text" placeholder="default" @keydown.enter="onSearch" />
          </label>
          <label class="field" style="margin: 0">
            <span>Name</span>
            <input v-model="filters.name" type="text" placeholder="nginx" @keydown.enter="onSearch" />
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
                <th class="sel">
                  <input
                    type="checkbox"
                    :checked="isAllVisibleSelected"
                    :indeterminate="isSomeVisibleSelected"
                    :disabled="loading || items.length === 0"
                    @change="toggleAllVisible"
                    aria-label="Select all"
                  />
                </th>
                <th>Name</th>
                <th>Namespace</th>
                <th class="num">Replicas</th>
                <th class="num">Running</th>
                <th class="num">Failed</th>
                <th>Phase</th>
                <th>Created</th>
                <th class="actions-col">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="loading">
                <td colspan="9" class="muted">Loading...</td>
              </tr>
              <tr v-else-if="items.length === 0">
                <td colspan="9" class="muted">No data</td>
              </tr>
              <tr v-else v-for="it in items" :key="`${it.namespace}/${it.name}`">
                <td class="sel">
                  <input
                    type="checkbox"
                    :checked="!!selected[`${it.namespace}/${it.name}`]"
                    :disabled="deleting"
                    @change="(e: any) => {
                      const k = `${it.namespace}/${it.name}`
                      const checked = !!e?.target?.checked
                      const m = { ...(selected || {}) }
                      if (checked) m[k] = true
                      else delete m[k]
                      selected = m
                    }"
                    aria-label="Select"
                  />
                </td>
                <td class="mono">{{ it.name }}</td>
                <td class="mono">{{ it.namespace }}</td>
                <td class="num">{{ it.replicas }}</td>
                <td class="num running">{{ it.runningReplicas }}</td>
                <td class="num failed">{{ it.failedReplicas }}</td>
                <td>
                  <span class="pill" :class="(it.phase || '').toLowerCase()">{{ it.phase || '-' }}</span>
                </td>
                <td class="mono">{{ (it.creationTimestamp || '').replace('T', ' ').replace('Z', '') }}</td>
                <td class="actions-col">
                  <button class="btn" @click="toDetails(it)">Details</button>
                  <button class="btn danger" @click="openDeleteConfirm(it)">Delete</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="pager">
          <div class="muted">Total: {{ total }}</div>
          <div class="pager-actions">
            <button class="btn" :disabled="loading || page <= 1" @click="toPrev">Prev</button>
            <div class="mono">Page {{ page }} / {{ totalPages }}</div>
            <button class="btn" :disabled="loading || page >= totalPages" @click="toNext">Next</button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="showCreate" class="modal-backdrop" @click.self="!creating && (showCreate = false)">
      <div class="modal">
        <div class="modal-header">
          <h2>{{ t('applications.create.title') }}</h2>
          <button class="icon-btn" :disabled="creating" @click="showCreate = false" aria-label="Close">×</button>
        </div>

        <div class="modal-body">
          <label class="field">
            <span>YAML</span>
            <textarea
              v-model="form.yamlText"
              rows="16"
              placeholder="apiVersion: core.kubex.io/v1alpha1\nkind: GlobalDeployment\nmetadata:\n  name: nginx\n  namespace: default\nspec:\n  replicas: 3\n  template:\n    apiVersion: apps/v1\n    kind: Deployment\n    metadata:\n      name: nginx\n    spec:\n      selector:\n        matchLabels:\n          app: nginx\n      template:\n        metadata:\n          labels:\n            app: nginx\n        spec:\n          containers:\n            - name: nginx\n              image: nginx"
            />
          </label>

          <p v-if="createError" class="error">{{ createError }}</p>
        </div>

        <div class="modal-footer">
          <button class="btn" :disabled="creating" @click="showCreate = false">{{ t('common.cancel') }}</button>
          <button class="btn primary" :disabled="creating" @click="onSubmitCreate">
            <span v-if="creating">{{ t('common.submitting') }}</span>
            <span v-else>{{ t('applications.create.submit') }}</span>
          </button>
        </div>
      </div>
    </div>

    <div v-if="showDeleteConfirm" class="modal-backdrop" @click.self="!deleting && (showDeleteConfirm = false)">
      <div class="modal">
        <div class="modal-header">
          <h2>Delete Application</h2>
          <button class="icon-btn" :disabled="deleting" @click="showDeleteConfirm = false" aria-label="Close">×</button>
        </div>

        <div class="modal-body">
          <p class="muted">This action can't be undone.</p>
          <p v-if="deleteTarget" class="mono">
            {{ deleteTarget.namespace }}/{{ deleteTarget.name }}
          </p>
          <p v-if="deleteError" class="error">{{ deleteError }}</p>
        </div>

        <div class="modal-footer">
          <button class="btn" :disabled="deleting" @click="showDeleteConfirm = false">{{ t('common.cancel') }}</button>
          <button class="btn danger" :disabled="deleting" @click="onConfirmDelete">
            <span v-if="deleting">{{ t('common.submitting') }}</span>
            <span v-else>Confirm delete</span>
          </button>
        </div>
      </div>
    </div>

    <div v-if="showBatchDeleteConfirm" class="modal-backdrop" @click.self="!deleting && (showBatchDeleteConfirm = false)">
      <div class="modal">
        <div class="modal-header">
          <h2>Delete Applications</h2>
          <button class="icon-btn" :disabled="deleting" @click="showBatchDeleteConfirm = false" aria-label="Close">×</button>
        </div>

        <div class="modal-body">
          <p class="muted">This action can't be undone.</p>
          <p class="muted">Selected: <span class="mono">{{ selectedCount }}</span></p>

          <div v-if="batchDeleteResult" class="card" style="margin-top: 10px">
            <div class="pad">
              <div class="ok" v-if="batchDeleteResult.failed === 0">
                Deleted {{ batchDeleteResult.deleted }} applications.
              </div>
              <div class="error" v-else>
                Deleted {{ batchDeleteResult.deleted }}, failed {{ batchDeleteResult.failed }}.
              </div>

              <div v-if="batchDeleteResult.failedItems.length" style="margin-top: 10px">
                <div class="muted" style="font-size: 12px; margin-bottom: 6px">Failures</div>
                <ul class="mono" style="margin: 0; padding-left: 18px">
                  <li v-for="f in batchDeleteResult.failedItems" :key="`${f.namespace}/${f.name}`">
                    {{ f.namespace }}/{{ f.name }}: {{ f.error }}
                  </li>
                </ul>
              </div>
            </div>
          </div>

          <p v-if="batchDeleteError" class="error">{{ batchDeleteError }}</p>
        </div>

        <div class="modal-footer">
          <button class="btn" :disabled="deleting" @click="showBatchDeleteConfirm = false">{{ t('common.cancel') }}</button>
          <button class="btn danger" :disabled="deleting || selectedCount === 0" @click="onConfirmBatchDelete">
            <span v-if="deleting">{{ t('common.submitting') }}</span>
            <span v-else>Confirm delete</span>
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
.sel {
  width: 44px;
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

.running {
  color: #065f46;
  font-weight: 700;
}
.failed {
  color: #991b1b;
  font-weight: 700;
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
.ok {
  color: #065f46;
  background: #ecfdf5;
  border: 1px solid #6ee7b7;
  padding: 8px 10px;
  border-radius: 10px;
}
.error {
  color: #b91c1c;
  background: #fef2f2;
  border: 1px solid #fecaca;
  padding: 8px 10px;
  border-radius: 10px;
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
  width: min(960px, 100%);
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
.icon-btn {
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 6px 10px;
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
  .filters {
    grid-template-columns: 1fr;
  }
  .filters-actions {
    justify-content: flex-start;
  }
}
</style>
