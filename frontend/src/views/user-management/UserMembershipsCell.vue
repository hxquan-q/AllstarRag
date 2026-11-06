<template>
  <div v-if="row.memberships?.length" class="user-table__memberships">
    <t-tag
      v-for="membership in visibleMemberships"
      :key="`${membership.tenant_id}-${membership.role}`"
      size="small"
      variant="light"
    >
      {{ membership.tenant_name }} · {{ roleLabel(membership.role) }}
    </t-tag>
    <t-tooltip v-if="hiddenMembershipCount > 0" :content="hiddenMembershipsText" placement="top">
      <t-tag size="small" variant="light">+{{ hiddenMembershipCount }}</t-tag>
    </t-tooltip>
  </div>
  <span v-else class="user-table__role-text">—</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { UserRow } from './types'

const MAX_MEMBERSHIP_TAGS = 2

const props = defineProps<{
  row: UserRow
  roleLabel: (role: string) => string
}>()

const visibleMemberships = computed(() =>
  (props.row.memberships ?? []).slice(0, MAX_MEMBERSHIP_TAGS),
)

const hiddenMembershipCount = computed(() =>
  Math.max(0, (props.row.memberships?.length ?? 0) - MAX_MEMBERSHIP_TAGS),
)

const hiddenMembershipsText = computed(() =>
  (props.row.memberships ?? [])
    .slice(MAX_MEMBERSHIP_TAGS)
    .map((membership) => `${membership.tenant_name} · ${props.roleLabel(membership.role)}`)
    .join('、'),
)
</script>

<style scoped lang="less">
.user-table__memberships {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 4px;
  overflow: hidden;
}

.user-table__role-text {
  color: var(--td-text-color-primary);
}
</style>
