<template>
  <div class="user-table__actions">
    <template v-if="row.role === 'system_admin'">
      <t-dropdown trigger="click" placement="bottom-right" :popup-props="{ zIndex: 6200 }">
        <t-button
          shape="square"
          variant="text"
          size="small"
          :aria-label="t('userManagement.actions.more')"
          @click.stop
        >
          <template #icon><t-icon name="ellipsis" /></template>
        </t-button>
        <template #dropdown>
          <t-dropdown-menu>
            <t-dropdown-item
              theme="error"
              :disabled="row.user_id === currentUserId"
              @click="emit('revoke-system-admin', row)"
            >
              {{ t('userManagement.actions.revokeSystemAdmin') }}
            </t-dropdown-item>
          </t-dropdown-menu>
        </template>
      </t-dropdown>
    </template>

    <template v-else>
      <t-tooltip :content="statusTooltip" placement="top">
        <t-button
          variant="text"
          size="small"
          :disabled="!statusGate.allowed"
          @click.stop="emitStatusAction"
        >
          {{ statusActionLabel }}
        </t-button>
      </t-tooltip>

      <!-- 多空间视图：改角色/交接不可用，改为切到账号所在空间再操作（Q44）。 -->
      <SwitchSpaceButton
        v-if="!canManageMembership"
        :memberships="row.memberships"
        :label="t('userManagement.actions.switchSpace')"
      />

      <t-dropdown trigger="click" placement="bottom-right" :popup-props="{ zIndex: 6200 }">
        <t-button
          shape="square"
          variant="text"
          size="small"
          :aria-label="t('userManagement.actions.more')"
          @click.stop
        >
          <template #icon><t-icon name="ellipsis" /></template>
        </t-button>
        <template #dropdown>
          <t-dropdown-menu>
            <t-dropdown-item
              v-if="row.role === 'owner' && canManageMembership"
              :disabled="!gate('transfer', row).allowed"
              @click="emit('transfer', row)"
            >
              {{ t('userManagement.actions.transfer') }}
            </t-dropdown-item>
            <t-dropdown-item
              v-if="row.role === 'owner' && isSystemAdmin"
              :disabled="row.user_id === currentUserId"
              @click="emit('promote-system-admin', row)"
            >
              {{ t('userManagement.actions.promoteSystemAdmin') }}
            </t-dropdown-item>
            <t-dropdown-item
              v-if="canResetPassword"
              :disabled="!resetPasswordAllowed"
              @click="emit('reset-password', row)"
            >
              {{ t('userManagement.resetPassword.action') }}
            </t-dropdown-item>
            <t-dropdown-item
              theme="error"
              :disabled="!gate('purge', row).allowed"
              @click="emit('purge', row)"
            >
              {{ t('userManagement.actions.purge') }}
            </t-dropdown-item>
          </t-dropdown-menu>
        </template>
      </t-dropdown>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { UserManagementListScope } from '@/api/user-management'
import {
  canManageMembershipInScope,
  gateUserAction,
  type ActionGate,
  type UserActionKind,
  type UserActionPermissions,
  type UserRow,
} from './types'
import SwitchSpaceButton from './SwitchSpaceButton.vue'

const props = defineProps<{
  row: UserRow
  currentUserId: string
  isSystemAdmin: boolean
  permissions: UserActionPermissions
  ownerCount: number
  canResetPassword: boolean
  /** 空间筛选口径：多空间视图禁用改角色/交接，改为「切换到该空间」。 */
  scope?: UserManagementListScope
  /** 由 UserTable 注入的 i18n 函数，避免子组件各自 useI18n。 */
  t: (key: string) => string
}>()

const canManageMembership = computed(() =>
  canManageMembershipInScope(props.scope ?? 'current'),
)

const emit = defineEmits<{
  ban: [row: UserRow]
  unban: [row: UserRow]
  purge: [row: UserRow]
  transfer: [row: UserRow]
  'promote-system-admin': [row: UserRow]
  'revoke-system-admin': [row: UserRow]
  'reset-password': [row: UserRow]
}>()

function gate(kind: UserActionKind, row: UserRow): ActionGate {
  return gateUserAction(
    kind,
    row,
    props.currentUserId,
    props.permissions,
    props.isSystemAdmin,
    props.ownerCount,
  )
}

const statusGate = computed(() =>
  gate(props.row.status === 'banned' ? 'unban' : 'ban', props.row),
)

const statusActionLabel = computed(() =>
  props.row.status === 'banned'
    ? props.t('userManagement.actions.unban')
    : props.t('userManagement.actions.ban'),
)

const statusTooltip = computed(() =>
  statusGate.value.reasonKey
    ? props.t(statusGate.value.reasonKey)
    : statusActionLabel.value,
)

/** 不能重置自己；owner 操作者不能重置其他普通管理员（与后端特权目标保护一致）。 */
const resetPasswordAllowed = computed(() => {
  if (props.row.user_id === props.currentUserId) return false
  if (props.row.status === 'deleted') return false
  if (props.row.role === 'owner' && !props.isSystemAdmin) return false
  return true
})

function emitStatusAction() {
  if (!statusGate.value.allowed) return
  if (props.row.status === 'banned') emit('unban', props.row)
  else emit('ban', props.row)
}
</script>

<style scoped lang="less">
.user-table__actions {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 2px;
}
</style>
