<template>
  <div class="yzj-mode" role="tablist" :aria-label="t('system.globalSettings.yunzhijiaSync.title')">
    <button
      v-for="tab in tabs"
      :key="tab.value"
      type="button"
      role="tab"
      class="yzj-mode__tab"
      :class="{ 'is-active': modelValue === tab.value }"
      :aria-selected="modelValue === tab.value"
      :data-testid="`yzj-sync-mode-${tab.value}`"
      @click="$emit('update:modelValue', tab.value)"
    >
      {{ tab.label }}（{{ tab.count }}）
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

type Mode = 'anomalies' | 'blocked'

const props = defineProps<{
  modelValue: Mode
  anomalies: number
  blocked: number
}>()

defineEmits<{ (e: 'update:modelValue', value: Mode): void }>()

const { t } = useI18n()

const tabs = computed(() => [
  {
    value: 'anomalies' as const,
    label: t('system.globalSettings.yunzhijiaSync.anomaliesTitle'),
    count: props.anomalies,
  },
  {
    value: 'blocked' as const,
    label: t('system.globalSettings.yunzhijiaSync.blockedTitle'),
    count: props.blocked,
  },
])
</script>

<style scoped>
/* 与成员列表的 Tab 一致：文字 + 蓝色下划线，不用填充胶囊/白底方块。 */
.yzj-mode {
  display: flex;
  align-items: center;
  gap: 24px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.yzj-mode__tab {
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
}

.yzj-mode__tab:hover {
  color: var(--td-text-color-primary);
}

.yzj-mode__tab.is-active {
  color: var(--td-brand-color);
  border-bottom-color: var(--td-brand-color);
  font-weight: 500;
}

.yzj-mode__tab:focus-visible {
  outline: 2px solid var(--td-brand-color);
  outline-offset: 2px;
}
</style>
