<template>
  <div class="user-table__account">
    <span class="user-table__account-email">{{ row.email || '—' }}</span>
    <span v-if="row.username" class="user-table__account-name">{{ row.username }}</span>
  </div>
</template>

<script setup lang="ts">
import type { UserRow } from './types'

defineProps<{ row: UserRow }>()
</script>

<style scoped lang="less">
.user-table__account {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.user-table__account-email {
  overflow: hidden;
  color: var(--td-text-color-primary);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-table__account-name {
  overflow: hidden;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
