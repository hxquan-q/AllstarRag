<template>
  <aside class="role-directory" :aria-label="title">
    <h2 class="role-directory__title">{{ title }}</h2>
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
import type { DirectoryEntry, DirectoryRole } from './types'

defineProps<{
  /** 目录标题（调用方传入已翻译文案）。 */
  title: string
  entries: DirectoryEntry[]
  selected: DirectoryRole
}>()

const emit = defineEmits<{
  select: [role: DirectoryRole]
}>()
</script>

<style scoped lang="less">
.role-directory {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: 208px;
  flex: 0 0 208px;
  min-height: 0;
  padding: 16px 12px;
  overflow-y: auto;
  background: var(--td-bg-color-secondarycontainer);
  border-radius: var(--td-radius-medium);
}

.role-directory__title {
  margin: 0;
  padding: 0 8px;
  font-size: var(--app-text-base);
  font-weight: 600;
  color: var(--td-text-color-secondary);
}

.role-directory__list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.role-directory__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 10px 12px;
  border: none;
  border-radius: var(--td-radius-medium);
  background: transparent;
  color: var(--td-text-color-primary);
  font-size: var(--app-text-base);
  line-height: 20px;
  text-align: left;
  cursor: pointer;
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

.role-directory__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.role-directory__count {
  flex: 0 0 auto;
  margin-left: 8px;
  color: var(--td-text-color-secondary);
  font-variant-numeric: tabular-nums;
}

.role-directory__item--active .role-directory__count {
  color: var(--td-brand-color);
}
</style>
