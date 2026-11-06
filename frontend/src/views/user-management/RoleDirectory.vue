<template>
  <aside class="role-directory" :aria-label="title">
    <div class="role-directory__head">
      <h2 class="role-directory__title">{{ title }}</h2>
      <!-- 空间筛选（Q34）：仅系统管理员视图渲染；owner 固定 current。 -->
      <t-select
        v-if="showScopeFilter"
        size="small"
        class="role-directory__scope"
        :value="scope"
        :options="scopeOptions"
        :popup-props="{ zIndex: 6200 }"
        :aria-label="t('userManagement.spaceFilter.title')"
        @change="(value: unknown) => emit('scope-change', value as UserManagementListScope)"
      />
    </div>
    <ul class="role-directory__list">
      <li v-for="entry in entries" :key="entry.key">
        <button
          type="button"
          class="role-directory__item"
          :class="{ 'role-directory__item--active': entry.key === selected }"
          :aria-current="entry.key === selected ? 'true' : undefined"
          @click="emit('select', entry.key)"
        >
          <span class="role-directory__label">{{ entry.label }}</span>
          <span class="role-directory__count">{{ entry.count }}</span>
        </button>
      </li>
    </ul>
  </aside>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UserManagementListScope } from '@/api/user-management'
import type { DirectoryEntry, DirectoryRole } from './types'

withDefaults(
  defineProps<{
    /** 目录标题（调用方传入已翻译文案）。 */
    title: string
    entries: DirectoryEntry[]
    selected: DirectoryRole
    /** 是否渲染右上角的空间筛选（仅系统管理员视图）。 */
    showScopeFilter?: boolean
    /** 当前空间筛选；owner 视图恒为 current。 */
    scope?: UserManagementListScope
  }>(),
  { showScopeFilter: false, scope: 'current' },
)

const emit = defineEmits<{
  select: [role: DirectoryRole]
  'scope-change': [scope: UserManagementListScope]
}>()

const { t } = useI18n()

const scopeOptions = computed(() => [
  { label: t('userManagement.spaceFilter.current'), value: 'current' },
  { label: t('userManagement.spaceFilter.other'), value: 'other' },
  { label: t('userManagement.spaceFilter.all'), value: 'all' },
])
</script>

<style scoped lang="less">
.role-directory {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 200px;
  flex: 0 0 200px;
  min-height: 0;
  padding: 12px;
  overflow-y: auto;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-lg);
}

.role-directory__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 4px 4px;
}

.role-directory__title {
  margin: 0;
  font-size: var(--app-text-base);
  font-weight: 600;
  color: var(--td-text-color-primary);
}

.role-directory__scope {
  width: 96px;
  flex: 0 0 auto;
}

.role-directory__list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.role-directory__item {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 8px 10px 8px 14px;
  border: none;
  border-radius: var(--app-radius-md);
  background: transparent;
  color: var(--td-text-color-primary);
  font-size: var(--app-text-base);
  line-height: 20px;
  text-align: left;
  cursor: pointer;
  transition: background var(--app-motion-fast) ease, color var(--app-motion-fast) ease;
}

.role-directory__item::before {
  position: absolute;
  top: 8px;
  bottom: 8px;
  left: 4px;
  width: 3px;
  border-radius: var(--app-radius-pill);
  background: transparent;
  content: '';
}

.role-directory__item:hover {
  background: var(--td-bg-color-container-hover);
}

.role-directory__item:focus-visible {
  outline: 2px solid var(--td-brand-color);
  outline-offset: -2px;
}

.role-directory__item--active {
  background: var(--td-brand-color-light);
  color: var(--td-brand-color);
  font-weight: 600;
}

.role-directory__item--active::before {
  background: var(--td-brand-color);
}

.role-directory__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.role-directory__count {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  margin-left: 8px;
  padding: 0 6px;
  border-radius: var(--app-radius-pill);
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-xs);
  font-variant-numeric: tabular-nums;
}

.role-directory__item--active .role-directory__count {
  background: var(--td-brand-color);
  color: var(--td-text-color-anti);
}
</style>
