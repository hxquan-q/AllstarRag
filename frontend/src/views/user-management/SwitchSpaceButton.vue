<template>
  <t-dropdown
    v-if="targets.length > 1"
    trigger="click"
    placement="bottom-right"
    :popup-props="{ zIndex: 6200 }"
  >
    <t-button variant="text" size="small" @click.stop>{{ label }}</t-button>
    <template #dropdown>
      <t-dropdown-menu>
        <t-dropdown-item
          v-for="target in targets"
          :key="target.tenantId"
          @click="switchTo(target)"
        >
          {{ target.label }}
        </t-dropdown-item>
      </t-dropdown-menu>
    </template>
  </t-dropdown>
  <t-button
    v-else-if="targets.length === 1"
    variant="text"
    size="small"
    @click.stop="switchToFirst"
  >
    {{ label }}
  </t-button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useAuthStore } from '@/stores/auth'
import {
  resolveTenantSwitchTargets,
  type TenantSwitchTarget,
  type UserRow,
} from './types'
import { switchToTargetTenant } from './switchSpace'

const props = defineProps<{
  memberships?: UserRow['memberships']
  /** 按钮文案，由父组件注入 i18n 结果。 */
  label: string
}>()

const auth = useAuthStore()
// 多空间视图里同一账号可能属于多个空间：排除当前空间后逐个列出。
const targets = computed(() =>
  resolveTenantSwitchTargets(props.memberships, auth.effectiveTenantId),
)

function switchTo(target: TenantSwitchTarget) {
  switchToTargetTenant(auth, target)
}

function switchToFirst() {
  const target = targets.value[0]
  if (target) switchTo(target)
}
</script>
