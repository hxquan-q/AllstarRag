<template>
  <section class="yzj-sync" :aria-label="t('system.globalSettings.yunzhijiaSync.title')">
    <header class="yzj-sync__header">
      <div class="yzj-sync__heading">
        <h3>{{ t('system.globalSettings.yunzhijiaSync.title') }}</h3>
        <p>{{ t('system.globalSettings.yunzhijiaSync.description') }}</p>
      </div>
      <div class="yzj-sync__actions">
        <t-button variant="outline" :loading="previewing" :disabled="busy" @click="onPreview">
          {{ t('system.globalSettings.yunzhijiaSync.preview') }}
        </t-button>
        <t-button theme="primary" :loading="syncing" :disabled="busy" @click="onSync">
          {{ t('system.globalSettings.yunzhijiaSync.syncNow') }}
        </t-button>
        <t-button variant="text" shape="square" :loading="loading" :disabled="busy"
          :title="t('system.globalSettings.yunzhijiaSync.refresh')"
          :aria-label="t('system.globalSettings.yunzhijiaSync.refresh')" @click="loadStatus">
          <template #icon><t-icon name="refresh" /></template>
        </t-button>
      </div>
    </header>

    <t-alert v-if="status && !status.enabled" theme="warning"
      :message="t('system.globalSettings.yunzhijiaSync.disabledHint')" />
    <t-alert v-if="loadError" theme="error" :message="loadError" />
    <t-alert v-if="syncError" theme="error" :message="syncError" />
    <t-alert v-if="previewError" theme="error" :message="previewError" />

    <div v-if="loading && !status" class="yzj-sync__state">
      <t-loading size="small" />
      <span>{{ t('system.globalSettings.yunzhijiaSync.loading') }}</span>
    </div>

    <template v-else-if="status">
      <section class="yzj-sync__card">
        <div class="yzj-sync__card-head">
          <h4>{{ t('system.globalSettings.yunzhijiaSync.latestTitle') }}</h4>
          <t-tag :theme="status.enabled ? 'success' : 'default'" variant="light" size="small">
            {{ t(status.enabled
              ? 'system.globalSettings.yunzhijiaSync.enabled'
              : 'system.globalSettings.yunzhijiaSync.disabled') }}
          </t-tag>
        </div>

        <template v-if="latest">
          <dl class="yzj-sync__facts">
            <div>
              <dt>{{ t('system.globalSettings.yunzhijiaSync.fields.startedAt') }}</dt>
              <dd>{{ formatDate(latest.started_at) }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.yunzhijiaSync.fields.trigger') }}</dt>
              <dd>{{ enumLabel('triggers', latest.trigger) }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.yunzhijiaSync.fields.status') }}</dt>
              <dd>{{ enumLabel('statuses', latest.status) }}</dd>
            </div>
            <div>
              <dt>{{ t('system.globalSettings.yunzhijiaSync.fields.duration') }}</dt>
              <dd>{{ formatDuration(latest.duration_ms) }}</dd>
            </div>
          </dl>

          <dl class="yzj-sync__counts">
            <div v-for="field in countFields" :key="field.key">
              <dt>{{ t(field.labelKey) }}</dt>
              <dd>{{ latest[field.key] }}</dd>
            </div>
          </dl>

          <p v-if="latest.error_summary" class="yzj-sync__error">
            {{ t('system.globalSettings.yunzhijiaSync.errorSummary') }}：{{ latest.error_summary }}
          </p>
        </template>
        <p v-else class="yzj-sync__empty">{{ t('system.globalSettings.yunzhijiaSync.noRuns') }}</p>
      </section>

      <section class="yzj-sync__block">
        <h4>
          {{ t('system.globalSettings.yunzhijiaSync.anomaliesTitle') }}
          <span class="yzj-sync__badge">{{ latest?.anomaly_count ?? 0 }}</span>
        </h4>
        <p v-if="anomalies.length === 0" class="yzj-sync__empty">
          {{ t('system.globalSettings.yunzhijiaSync.anomaliesEmpty') }}
        </p>
        <table v-else class="yzj-sync__table">
          <thead>
            <tr>
              <th>{{ t('system.globalSettings.yunzhijiaSync.anomalyColumns.kind') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.anomalyColumns.name') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.anomalyColumns.detail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(item, index) in anomalies" :key="`${item.kind}-${item.open_id || item.name || index}`">
              <td>{{ enumLabel('anomalyKinds', item.kind) }}</td>
              <td>{{ item.name || '—' }}</td>
              <td>{{ item.detail || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="yzj-sync__block">
        <h4>
          {{ t('system.globalSettings.yunzhijiaSync.blockedTitle') }}
          <span class="yzj-sync__badge">{{ blocked.length }}</span>
        </h4>
        <p v-if="blocked.length === 0" class="yzj-sync__empty">
          {{ t('system.globalSettings.yunzhijiaSync.blockedEmpty') }}
        </p>
        <table v-else class="yzj-sync__table">
          <thead>
            <tr>
              <th>{{ t('system.globalSettings.yunzhijiaSync.blockedColumns.openId') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.blockedColumns.name') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.blockedColumns.department') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.blockedColumns.blockedAt') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.blockedColumns.blockedBy') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.blockedColumns.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in blocked" :key="row.open_id">
              <td class="yzj-sync__mono">{{ row.open_id }}</td>
              <td>{{ row.name || '—' }}</td>
              <td>{{ row.department || '—' }}</td>
              <td>{{ formatDate(row.blocked_at) }}</td>
              <td>{{ row.blocked_by || '—' }}</td>
              <td>
                <t-popconfirm :content="t('system.globalSettings.yunzhijiaSync.unblockConfirm', { name: row.name })"
                  theme="warning" placement="left"
                  :confirm-btn="{ content: t('system.globalSettings.yunzhijiaSync.unblock'), theme: 'danger' }"
                  :cancel-btn="t('common.cancel')" @confirm="onUnblock(row)">
                  <t-button variant="text" size="small" :loading="unblockingOpenId === row.open_id" :disabled="busy">
                    {{ t('system.globalSettings.yunzhijiaSync.unblock') }}
                  </t-button>
                </t-popconfirm>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section v-if="previewResult" class="yzj-sync__block">
        <h4>{{ t('system.globalSettings.yunzhijiaSync.previewTitle') }}</h4>
        <dl class="yzj-sync__counts">
          <div>
            <dt>{{ t('system.globalSettings.yunzhijiaSync.previewActive') }}</dt>
            <dd>{{ previewResult.active_count }}</dd>
          </div>
          <div>
            <dt>{{ t('system.globalSettings.yunzhijiaSync.previewInactive') }}</dt>
            <dd>{{ previewResult.inactive_count }}</dd>
          </div>
          <div>
            <dt>{{ t('system.globalSettings.yunzhijiaSync.previewTenantDefault') }}</dt>
            <dd>{{ previewResult.tenant_default_count }}</dd>
          </div>
          <div>
            <dt>{{ t('system.globalSettings.yunzhijiaSync.previewTenantHR') }}</dt>
            <dd>{{ previewResult.tenant_hr_count }}</dd>
          </div>
        </dl>

        <h5>
          {{ t('system.globalSettings.yunzhijiaSync.previewAnomalies') }}
          <span class="yzj-sync__badge">{{ previewAnomalies.length }}</span>
        </h5>
        <p v-if="previewAnomalies.length === 0" class="yzj-sync__empty">
          {{ t('system.globalSettings.yunzhijiaSync.anomaliesEmpty') }}
        </p>
        <table v-else class="yzj-sync__table">
          <thead>
            <tr>
              <th>{{ t('system.globalSettings.yunzhijiaSync.anomalyColumns.kind') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.anomalyColumns.name') }}</th>
              <th>{{ t('system.globalSettings.yunzhijiaSync.anomalyColumns.detail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(item, index) in previewAnomalies"
              :key="`preview-${item.kind}-${item.open_id || item.name || index}`">
              <td>{{ enumLabel('anomalyKinds', item.kind) }}</td>
              <td>{{ item.name || '—' }}</td>
              <td>{{ item.detail || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  getYunzhijiaSyncStatus,
  previewYunzhijiaSync,
  runYunzhijiaSync,
  unblockYunzhijiaEmployee,
  type YunzhijiaAdminStatus,
  type YunzhijiaBlockedEmployee,
  type YunzhijiaPreviewResult,
} from '@/api/system/yunzhijia'

const { t, te, locale } = useI18n()

const status = ref<YunzhijiaAdminStatus | null>(null)
const previewResult = ref<YunzhijiaPreviewResult | null>(null)
const loading = ref(false)
const previewing = ref(false)
const syncing = ref(false)
const unblockingOpenId = ref<string | null>(null)
const loadError = ref('')
const previewError = ref('')
const syncError = ref('')

const latest = computed(() => status.value?.latest ?? null)
const anomalies = computed(() => latest.value?.anomalies ?? [])
const blocked = computed(() => status.value?.blocked ?? [])
const previewAnomalies = computed(() => previewResult.value?.anomalies ?? [])
const busy = computed(() => loading.value || previewing.value || syncing.value)

const countFields = [
  { key: 'active_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.activeCount' },
  { key: 'inactive_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.inactiveCount' },
  { key: 'created_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.createdCount' },
  { key: 'updated_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.updatedCount' },
  { key: 'disabled_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.disabledCount' },
  { key: 'migrated_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.migratedCount' },
  { key: 'anomaly_count', labelKey: 'system.globalSettings.yunzhijiaSync.fields.anomalyCount' },
] as const

function enumLabel(group: 'triggers' | 'statuses' | 'anomalyKinds', value: string): string {
  const path = `system.globalSettings.yunzhijiaSync.${group}.${value}`
  return te(path) ? (t(path) as string) : value
}

function formatDate(value?: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString(locale.value, { hour12: false })
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
  previewError.value = ''
  previewResult.value = null
  try {
    previewResult.value = await previewYunzhijiaSync()
  } catch (error) {
    previewError.value = resolveError(error, 'system.globalSettings.yunzhijiaSync.messages.previewFailed')
  } finally {
    previewing.value = false
  }
}

async function onSync() {
  syncing.value = true
  syncError.value = ''
  try {
    await runYunzhijiaSync()
    MessagePlugin.success(t('system.globalSettings.yunzhijiaSync.messages.syncSuccess'))
  } catch (error) {
    syncError.value = resolveError(error, 'system.globalSettings.yunzhijiaSync.messages.syncFailed')
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

<style lang="less" scoped>
.yzj-sync {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-top: 24px;
  padding-top: 24px;
  border-top: 1px solid var(--td-component-stroke);
}
.yzj-sync__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}
.yzj-sync__heading {
  min-width: 0;
  h3 {
    margin: 0;
    font-size: var(--app-text-lg);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }
  p {
    margin: 4px 0 0;
    font-size: var(--app-text-sm);
    color: var(--td-text-color-secondary);
  }
}
.yzj-sync__actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.yzj-sync__card,
.yzj-sync__block {
  padding: 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-sm);
}
.yzj-sync__card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  h4,
  h5 {
    margin: 0;
    font-size: var(--app-text-md);
    font-weight: 600;
  }
}
.yzj-sync__block h4,
.yzj-sync__block h5 {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 10px;
  font-size: var(--app-text-md);
  font-weight: 600;
}
.yzj-sync__block h5 {
  margin-top: 16px;
  font-size: var(--app-text-sm);
}
.yzj-sync__facts,
.yzj-sync__counts {
  display: grid;
  gap: 12px;
  margin: 0;
  div {
    min-width: 0;
  }
  dt {
    font-size: var(--app-text-sm);
    color: var(--td-text-color-placeholder);
  }
  dd {
    margin: 2px 0 0;
    font-size: var(--app-text-md);
    color: var(--td-text-color-primary);
    overflow-wrap: anywhere;
  }
}
.yzj-sync__facts {
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
}
.yzj-sync__counts {
  grid-template-columns: repeat(auto-fit, minmax(110px, 1fr));
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px dashed var(--td-component-stroke);
}
.yzj-sync__error {
  margin: 12px 0 0;
  font-size: var(--app-text-sm);
  color: var(--td-error-color);
  overflow-wrap: anywhere;
}
.yzj-sync__empty {
  margin: 0;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-placeholder);
}
.yzj-sync__state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 24px 0;
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-sm);
}
.yzj-sync__badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  border-radius: var(--app-radius-pill);
  background: var(--td-brand-color-light);
  color: var(--td-brand-color);
  font-size: var(--app-text-xs);
  font-weight: 500;
}
.yzj-sync__table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--app-text-sm);
  th,
  td {
    padding: 8px 10px;
    text-align: left;
    border-bottom: 1px solid var(--td-component-stroke);
    vertical-align: top;
  }
  th {
    color: var(--td-text-color-secondary);
    font-weight: 500;
    white-space: nowrap;
  }
  td {
    color: var(--td-text-color-primary);
    overflow-wrap: anywhere;
  }
  tr:last-child td {
    border-bottom: none;
  }
}
.yzj-sync__mono {
  font-family: var(--td-font-family-mono);
  font-size: var(--app-text-xs);
}
@media (max-width: 860px) {
  .yzj-sync__table {
    display: block;
    overflow-x: auto;
  }
}
</style>
