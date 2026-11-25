<template>
  <div class="yzj-sync">
    <!-- 顶部一行：左侧状态摘要，右侧操作按钮，与「操作记录」工具条同一套风格。 -->
    <div class="yzj-sync__toolbar">
      <div class="yzj-sync__summary">
        <div class="yzj-sync__status-line">
          <t-tag
            v-if="status"
            :theme="status.enabled ? 'success' : 'default'"
            variant="light"
            size="small"
          >
            {{ t(status.enabled
              ? 'system.globalSettings.yunzhijiaSync.enabled'
              : 'system.globalSettings.yunzhijiaSync.disabled') }}
          </t-tag>
          <t-loading v-if="isRunning" size="small" />
          <span v-if="latest" class="yzj-sync__muted">
            {{ t('system.globalSettings.yunzhijiaSync.latestTitle') }}：{{ formatTime(latest.started_at) }} ·
            {{ enumLabel('triggers', latest.trigger) }} · {{ enumLabel('statuses', latest.status) }} ·
            {{ formatDuration(latest.duration_ms) }}
          </span>
          <span v-else class="yzj-sync__muted">
            {{ t('system.globalSettings.yunzhijiaSync.noRuns') }}
          </span>
        </div>
        <p class="yzj-sync__muted yzj-sync__hint">
          {{ t('system.globalSettings.yunzhijiaSync.description') }}
        </p>
        <p v-if="latest?.error_summary" class="yzj-sync__error">
          {{ t('system.globalSettings.yunzhijiaSync.errorSummary') }}：{{ latest.error_summary }}
        </p>
      </div>

      <div class="yzj-sync__actions">
        <t-button variant="outline" :loading="previewing" :disabled="busy" @click="onPreview">
          {{ t('system.globalSettings.yunzhijiaSync.preview') }}
        </t-button>
        <t-button theme="primary" :loading="syncing" :disabled="busy" @click="onSync">
          {{ t('system.globalSettings.yunzhijiaSync.syncNow') }}
        </t-button>
        <t-button variant="outline" :loading="loading" :disabled="busy" @click="loadStatus">
          {{ t('system.globalSettings.yunzhijiaSync.refresh') }}
        </t-button>
      </div>
    </div>

    <p v-if="previewing" class="yzj-sync__muted yzj-sync__hint">
      {{ t('system.globalSettings.yunzhijiaSync.previewHint') }}
    </p>

    <t-alert
      v-if="status && !status.enabled"
      theme="info"
      :message="t('system.globalSettings.yunzhijiaSync.disabledHint')"
    />
    <t-alert v-if="loadError" theme="error" :message="loadError">
      <template #operation>
        <t-button size="small" @click="loadStatus">{{ t('common.retry') }}</t-button>
      </template>
    </t-alert>

    <div v-if="loading && !status" class="yzj-sync__state">
      <t-loading size="small" />
      <span>{{ t('system.globalSettings.yunzhijiaSync.loading') }}</span>
    </div>

    <template v-else-if="status">
      <!-- 最近一次同步的计数：不套卡片，数字大一号；0 淡色，异常用警示色。 -->
      <dl class="yzj-sync__stats">
        <div
          v-for="field in countFields"
          :key="field.key"
          :class="{
            'is-zero': !latest || latest[field.key] === 0,
            'is-warning': field.key === 'anomaly_count' && (latest?.anomaly_count ?? 0) > 0,
          }"
        >
          <dt>{{ t(field.labelKey) }}</dt>
          <dd>{{ latest ? latest[field.key] : '—' }}</dd>
        </div>
      </dl>

      <!-- 异常 / 禁止重建 分段切换，同一时间只展示一张表。 -->
      <div class="yzj-sync__section">
        <div class="yzj-sync__mode" role="tablist" :aria-label="t('system.globalSettings.yunzhijiaSync.title')">
          <button
            type="button"
            role="tab"
            class="yzj-sync__mode-tab"
            :class="{ 'is-active': tableMode === 'anomalies' }"
            :aria-selected="tableMode === 'anomalies'"
            data-testid="yzj-sync-mode-anomalies"
            @click="tableMode = 'anomalies'"
          >
            {{ t('system.globalSettings.yunzhijiaSync.anomaliesTitle') }}（{{ anomalies.length }}）
          </button>
          <button
            type="button"
            role="tab"
            class="yzj-sync__mode-tab"
            :class="{ 'is-active': tableMode === 'blocked' }"
            :aria-selected="tableMode === 'blocked'"
            data-testid="yzj-sync-mode-blocked"
            @click="tableMode = 'blocked'"
          >
            {{ t('system.globalSettings.yunzhijiaSync.blockedTitle') }}（{{ blocked.length }}）
          </button>
        </div>

        <template v-if="tableMode === 'anomalies'">
          <t-table
            v-if="anomalies.length > 0"
            class="yzj-sync__table"
            :data="anomalies"
            :columns="anomalyColumns"
            :row-key="anomalyRowKey"
            :pagination="{ defaultPageSize: 10 }"
            size="medium"
            hover
          >
            <template #kind="{ row }">
              <t-tag size="small" variant="light" :theme="anomalyTagTheme(row.kind)">
                {{ enumLabel('anomalyKinds', row.kind) }}
              </t-tag>
            </template>
            <template #name="{ row }">{{ row.name || '—' }}</template>
            <template #detail="{ row }">
              <span class="yzj-sync__ellipsis" :title="row.detail || ''">{{ row.detail || '—' }}</span>
            </template>
          </t-table>
          <t-empty v-else :description="t('system.globalSettings.yunzhijiaSync.anomaliesEmpty')" />
        </template>

        <template v-else>
          <t-table
            v-if="blocked.length > 0"
            class="yzj-sync__table"
            :data="blocked"
            :columns="blockedColumns"
            row-key="open_id"
            :pagination="{ defaultPageSize: 10 }"
            size="medium"
            hover
          >
            <template #name="{ row }">
              <div class="yzj-sync__person">
                <span>{{ row.name || '—' }}</span>
                <span class="yzj-sync__muted yzj-sync__mono">{{ row.open_id }}</span>
              </div>
            </template>
            <template #department="{ row }">{{ row.department || '—' }}</template>
            <template #blocked_at="{ row }">{{ formatTime(row.blocked_at) }}</template>
            <template #blocked_by="{ row }">{{ displayUser(row.blocked_by) }}</template>
            <template #actions="{ row }">
              <t-popconfirm
                :content="t('system.globalSettings.yunzhijiaSync.unblockConfirm', { name: row.name })"
                theme="warning"
                placement="left"
                :confirm-btn="{ content: t('system.globalSettings.yunzhijiaSync.unblock'), theme: 'danger' }"
                :cancel-btn="t('common.cancel')"
                @confirm="onUnblock(row)"
              >
                <t-button
                  variant="text"
                  size="small"
                  :loading="unblockingOpenId === row.open_id"
                  :disabled="busy"
                >
                  {{ t('system.globalSettings.yunzhijiaSync.unblock') }}
                </t-button>
              </t-popconfirm>
            </template>
          </t-table>
          <t-empty v-else :description="t('system.globalSettings.yunzhijiaSync.blockedEmpty')" />
        </template>
      </div>
    </template>

    <YunzhijiaPreviewDialog
      :visible="previewResult !== null"
      :result="previewResult"
      @close="previewResult = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { listUsers } from '@/api/user-management'
import {
  getYunzhijiaSyncStatus,
  previewYunzhijiaSync,
  runYunzhijiaSync,
  unblockYunzhijiaEmployee,
  type YunzhijiaAdminStatus,
  type YunzhijiaBlockedEmployee,
  type YunzhijiaPreviewResult,
  type YunzhijiaSyncAnomaly,
} from '@/api/system/yunzhijia'
import YunzhijiaPreviewDialog from './YunzhijiaPreviewDialog.vue'

const props = defineProps<{ tenantId?: number | null }>()

const { t, te, locale } = useI18n()

const status = ref<YunzhijiaAdminStatus | null>(null)
const userNames = ref<Record<string, string>>({})
const previewResult = ref<YunzhijiaPreviewResult | null>(null)
const loading = ref(false)
const previewing = ref(false)
const syncing = ref(false)
const unblockingOpenId = ref<string | null>(null)
const loadError = ref('')
const tableMode = ref<'anomalies' | 'blocked'>('anomalies')

const latest = computed(() => status.value?.latest ?? null)
const anomalies = computed(() => latest.value?.anomalies ?? [])
const blocked = computed(() => status.value?.blocked ?? [])
const busy = computed(() => loading.value || previewing.value || syncing.value)
const isRunning = computed(() => latest.value?.status === 'running')

const countFields = [
  { key: 'active_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.activeCount' },
  { key: 'inactive_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.inactiveCount' },
  { key: 'created_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.createdCount' },
  { key: 'updated_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.updatedCount' },
  { key: 'disabled_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.disabledCount' },
  { key: 'migrated_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.migratedCount' },
  { key: 'anomaly_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.anomalyCount' },
] as const

const anomalyColumns = computed(() => [
  { colKey: 'kind', title: t('system.globalSettings.yunzhijiaSync.anomalyColumns.kind'), width: 140 },
  { colKey: 'name', title: t('system.globalSettings.yunzhijiaSync.anomalyColumns.name'), width: 160, ellipsis: true },
  { colKey: 'detail', title: t('system.globalSettings.yunzhijiaSync.anomalyColumns.detail'), ellipsis: true },
])

const blockedColumns = computed(() => [
  { colKey: 'name', title: t('system.globalSettings.yunzhijiaSync.blockedColumns.name'), width: 200, ellipsis: true },
  { colKey: 'department', title: t('system.globalSettings.yunzhijiaSync.blockedColumns.department'), width: 160, ellipsis: true },
  { colKey: 'blocked_at', title: t('system.globalSettings.yunzhijiaSync.blockedColumns.blockedAt'), width: 180 },
  { colKey: 'blocked_by', title: t('system.globalSettings.yunzhijiaSync.blockedColumns.blockedBy'), width: 160, ellipsis: true },
  { colKey: 'actions', title: t('system.globalSettings.yunzhijiaSync.blockedColumns.actions'), width: 90 },
])

function anomalyRowKey(row: YunzhijiaSyncAnomaly, index: number): string {
  return `${row.kind}-${row.open_id || row.name || index}`
}

function anomalyTagTheme(kind: string): 'warning' | 'default' {
  return kind === 'duplicate_name' || kind === 'provision_failed' ? 'warning' : 'default'
}

function enumLabel(group: 'triggers' | 'statuses' | 'anomalyKinds', value: string): string {
  const path = `system.globalSettings.yunzhijiaSync.${group}.${value}`
  return te(path) ? (t(path) as string) : value
}

// 操作人只回传 user_id；照「操作记录」用本空间成员列表回填邮箱/用户名，
// 查不到的显示原始 id，不回退成空。
async function loadUserNames(tenantId: number) {
  try {
    const resp = await listUsers(tenantId, { page: 1, page_size: 100 })
    const map: Record<string, string> = {}
    for (const item of resp.data?.items ?? []) {
      map[item.user_id] = item.email || item.username || item.user_id
    }
    userNames.value = map
  } catch {
    // 回填失败不影响记录本身展示。
  }
}

function displayUser(userId?: string): string {
  if (!userId) return '—'
  return userNames.value[userId] || userId
}

watch(
  () => props.tenantId,
  (tenantId) => {
    if (tenantId) void loadUserNames(tenantId)
  },
  { immediate: true },
)

// 时间格式与「操作记录」一致，避免同一页出现两种时间样式。
function formatTime(value?: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value)
}

function formatDuration(ms?: number): string {
  if (ms == null || ms < 0) return '—'
  return t('system.globalSettings.yunzhijiaSync.durationValue', {
    seconds: (ms / 1000).toFixed(1),
  }) as string
}

function resolveError(error: unknown, fallbackKey: string): string {
  const statusCode = (error as { status?: number } | null)?.status
  if (statusCode === 409) return t('system.globalSettings.yunzhijiaSync.messages.running') as string
  if (statusCode === 403) return t('system.globalSettings.yunzhijiaSync.messages.forbidden') as string
  return t(fallbackKey) as string
}

async function loadStatus() {
  loading.value = true
  loadError.value = ''
  try {
    status.value = await getYunzhijiaSyncStatus()
  } catch (error) {
    loadError.value = resolveError(error, 'system.globalSettings.yunzhijiaSync.loadFailed')
  } finally {
    loading.value = false
  }
}

async function onPreview() {
  previewing.value = true
  previewResult.value = null
  try {
    previewResult.value = await previewYunzhijiaSync()
  } catch (error) {
    MessagePlugin.error(resolveError(error, 'system.globalSettings.yunzhijiaSync.messages.previewFailed'))
  } finally {
    previewing.value = false
  }
}

async function onSync() {
  syncing.value = true
  try {
    await runYunzhijiaSync()
    MessagePlugin.success(t('system.globalSettings.yunzhijiaSync.messages.syncSuccess'))
  } catch (error) {
    MessagePlugin.error(resolveError(error, 'system.globalSettings.yunzhijiaSync.messages.syncFailed'))
  } finally {
    syncing.value = false
    // 成功、失败、超时、409 都刷新一次状态卡：后端同步可能已在后台完成，
    // 刷新后如实显示 running/failed，而不是停在上一轮。
    await loadStatus()
  }
}

async function onUnblock(row: YunzhijiaBlockedEmployee) {
  unblockingOpenId.value = row.open_id
  try {
    await unblockYunzhijiaEmployee(row.open_id)
    MessagePlugin.success(t('system.globalSettings.yunzhijiaSync.messages.unblockSuccess'))
    await loadStatus()
  } catch (error) {
    MessagePlugin.error(resolveError(error, 'system.globalSettings.yunzhijiaSync.messages.unblockFailed'))
  } finally {
    unblockingOpenId.value = null
  }
}

onMounted(loadStatus)
</script>

<style scoped lang="less">
.yzj-sync {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
  min-width: 0;
}

.yzj-sync__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--app-space-3);
}

.yzj-sync__summary {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  min-width: 0;
}

.yzj-sync__status-line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--app-space-2);
}

.yzj-sync__muted {
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
}

.yzj-sync__hint {
  margin: 0;
}

.yzj-sync__error {
  margin: 0;
  font-size: var(--app-text-sm);
  color: var(--td-error-color);
  overflow-wrap: anywhere;
}

.yzj-sync__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: var(--app-space-2);
}

.yzj-sync__state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--app-space-2);
  padding: var(--app-space-6) 0;
  color: var(--td-text-color-secondary);
}

.yzj-sync__stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(96px, 1fr));
  gap: var(--app-space-4);
  margin: 0;

  div {
    min-width: 0;
  }

  dt {
    font-size: var(--app-text-sm);
    color: var(--td-text-color-secondary);
  }

  dd {
    margin: 2px 0 0;
    font-size: var(--app-text-xl);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }

  .is-zero dd {
    font-weight: 500;
    color: var(--td-text-color-placeholder);
  }

  .is-warning dd {
    color: var(--td-warning-color);
  }
}

.yzj-sync__section {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
  min-width: 0;
}

/* 与成员列表的 Tab 一致：文字 + 蓝色下划线，不用填充胶囊/白底方块。 */
.yzj-sync__mode {
  display: flex;
  align-items: center;
  gap: 24px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.yzj-sync__mode-tab {
  appearance: none;
  margin: 0 0 -1px;
  padding: 8px 0 10px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  font: inherit;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
  cursor: pointer;
  transition: color 0.2s, border-color 0.2s;

  &:hover {
    color: var(--td-text-color-primary);
  }

  &.is-active {
    color: var(--td-brand-color);
    border-bottom-color: var(--td-brand-color);
    font-weight: 500;
  }

  &:focus-visible {
    outline: 2px solid var(--td-brand-color);
    outline-offset: 2px;
  }
}

.yzj-sync__table {
  min-width: 0;

  // 窄屏时表格横向滚动，不撑破 Tab 内容区。
  :deep(.t-table__content) {
    overflow-x: auto;
  }
}

.yzj-sync__ellipsis {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.yzj-sync__person {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.yzj-sync__mono {
  overflow: hidden;
  font-family: var(--app-font-family-mono);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
