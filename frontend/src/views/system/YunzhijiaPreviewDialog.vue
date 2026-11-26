<template>
  <t-dialog
    :visible="visible"
    width="760px"
    :header="t('system.globalSettings.yunzhijiaSync.previewTitle')"
    @update:visible="onVisibleUpdate"
  >
    <template v-if="result">
      <dl class="yzj-preview__stats">
        <div v-for="item in stats" :key="item.key">
          <dt>{{ t(item.labelKey) }}</dt>
          <dd>{{ result[item.key] }}</dd>
        </div>
      </dl>

      <h4 class="yzj-preview__title">
        {{ t('system.globalSettings.yunzhijiaSync.previewAnomalies') }}
        <span class="yzj-preview__count">{{ result.anomalies.length }}</span>
      </h4>

      <t-table
        v-if="result.anomalies.length > 0"
        :data="result.anomalies"
        :columns="columns"
        size="medium"
        hover
        max-height="360"
      >
        <template #kind="{ row }">
          <t-tag size="small" variant="light" :theme="tagTheme(row.kind)">
            {{ enumLabel(row.kind) }}
          </t-tag>
        </template>
        <template #name="{ row }">{{ row.name || '—' }}</template>
        <template #detail="{ row }">
          <span class="yzj-preview__ellipsis" :title="row.detail || ''">{{ row.detail || '—' }}</span>
        </template>
      </t-table>
      <t-empty v-else :description="t('system.globalSettings.yunzhijiaSync.anomaliesEmpty')" />
    </template>

    <template #footer>
      <t-button theme="primary" @click="emit('close')">
        {{ t('common.close') }}
      </t-button>
    </template>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { YunzhijiaPreviewResult } from '@/api/system/yunzhijia'

defineProps<{
  visible: boolean
  result: YunzhijiaPreviewResult | null
}>()

const emit = defineEmits<{ close: [] }>()

const { t, te } = useI18n()

const stats = [
  { key: 'active_count', labelKey: 'system.globalSettings.yunzhijiaSync.previewActive' },
  { key: 'inactive_count', labelKey: 'system.globalSettings.yunzhijiaSync.previewInactive' },
  { key: 'tenant_default_count', labelKey: 'system.globalSettings.yunzhijiaSync.previewTenantDefault' },
  { key: 'tenant_hr_count', labelKey: 'system.globalSettings.yunzhijiaSync.previewTenantHR' },
] as const

const columns = computed(() => [
  { colKey: 'kind', title: t('system.globalSettings.yunzhijiaSync.anomalyColumns.kind'), width: 140 },
  { colKey: 'name', title: t('system.globalSettings.yunzhijiaSync.anomalyColumns.name'), width: 160, ellipsis: true },
  { colKey: 'detail', title: t('system.globalSettings.yunzhijiaSync.anomalyColumns.detail'), ellipsis: true },
])

function tagTheme(kind: string): 'warning' | 'default' {
  return kind === 'duplicate_name' || kind === 'provision_failed' ? 'warning' : 'default'
}

function enumLabel(kind: string): string {
  const path = `system.globalSettings.yunzhijiaSync.anomalyKinds.${kind}`
  return te(path) ? (t(path) as string) : kind
}

function onVisibleUpdate(value: boolean) {
  if (!value) emit('close')
}
</script>

<style scoped lang="less">
.yzj-preview__stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: var(--app-space-4);
  margin: 0 0 var(--app-space-5);

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
}

.yzj-preview__title {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  margin: 0 0 var(--app-space-3);
  font-size: var(--app-text-md);
  font-weight: 600;
}

.yzj-preview__count {
  padding: 0 8px;
  font-size: var(--app-text-sm);
  font-weight: 500;
  color: var(--td-text-color-secondary);
  background: var(--td-bg-color-secondarycontainer);
  border-radius: var(--app-radius-pill);
}

.yzj-preview__ellipsis {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
