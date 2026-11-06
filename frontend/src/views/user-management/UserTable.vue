<template>
  <div class="user-table">
    <div v-if="loading && items.length === 0" class="user-table__state">
      <t-loading size="small" />
      <span>{{ t('userManagement.states.loading') }}</span>
    </div>

    <div v-else-if="error" class="user-table__state">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="emit('retry')">
            {{ t('userManagement.states.retry') }}
          </t-button>
        </template>
      </t-alert>
    </div>

    <div v-else-if="total === 0" class="user-table__state">
      <t-empty :description="t('userManagement.states.empty')" />
    </div>

    <template v-else>
      <div class="user-table__scroll">
        <t-table
          row-key="user_id"
          :data="items"
          :columns="columns"
          :loading="loading"
          size="medium"
          hover
        >
          <template #role="{ row }">
            <t-tooltip
              v-if="roleGate(row).reasonKey"
              :content="t(roleGate(row).reasonKey ?? '')"
              placement="top"
            >
              <span class="user-table__role-text">{{ roleLabel(row.role) }}</span>
            </t-tooltip>
            <t-select
              v-else-if="canChangeRole(row)"
              :model-value="row.role"
              size="small"
              :options="roleOptions"
              :popup-props="{ zIndex: 6200 }"
              @change="(value: unknown) => onRoleChange(row, value)"
            />
            <span v-else class="user-table__role-text">{{ roleLabel(row.role) }}</span>
          </template>
          <template #status="{ row }">
            <t-tag
              :theme="row.status === 'active' ? 'success' : 'danger'"
              variant="light"
            >
              {{ statusLabel(row.status) }}
            </t-tag>
          </template>
          <template #created_at="{ row }">{{ formatDateTime(row.created_at) }}</template>
          <template #actions="{ row }">
            <div class="user-table__actions">
              <template v-if="row.role === 'system_admin'">
                <t-tooltip :content="revokeTooltip(row)" placement="top">
                  <t-button
                    shape="square"
                    variant="text"
                    size="small"
                    theme="danger"
                    :disabled="row.user_id === currentUserId"
                    :aria-label="t('userManagement.actions.revokeSystemAdmin')"
                    @click.stop="emit('revoke-system-admin', row)"
                  >
                    <template #icon><t-icon name="user-clear" /></template>
                  </t-button>
                </t-tooltip>
              </template>

              <template v-else>
                <t-tooltip :content="statusTooltip(row)" placement="top">
                  <t-button
                    shape="square"
                    variant="text"
                    size="small"
                    :disabled="!statusGate(row).allowed"
                    :aria-label="statusActionLabel(row)"
                    @click.stop="emitStatusAction(row)"
                  >
                    <template #icon>
                      <t-icon :name="row.status === 'banned' ? 'rollback' : 'lock-on'" />
                    </template>
                  </t-button>
                </t-tooltip>

                <t-tooltip :content="actionTooltip('purge', row)" placement="top">
                  <t-button
                    shape="square"
                    variant="text"
                    size="small"
                    theme="danger"
                    :disabled="!gate('purge', row).allowed"
                    :aria-label="t('userManagement.actions.purge')"
                    @click.stop="emit('purge', row)"
                  >
                    <template #icon><t-icon name="delete" /></template>
                  </t-button>
                </t-tooltip>

                <t-tooltip
                  v-if="row.role === 'owner'"
                  :content="actionTooltip('transfer', row)"
                  placement="top"
                >
                  <t-button
                    shape="square"
                    variant="text"
                    size="small"
                    :disabled="!gate('transfer', row).allowed"
                    :aria-label="t('userManagement.actions.transfer')"
                    @click.stop="emit('transfer', row)"
                  >
                    <template #icon><t-icon name="swap" /></template>
                  </t-button>
                </t-tooltip>

                <t-tooltip
                  v-if="row.role === 'owner' && isSystemAdmin"
                  :content="promoteTooltip(row)"
                  placement="top"
                >
                  <t-button
                    shape="square"
                    variant="text"
                    size="small"
                    :disabled="row.user_id === currentUserId"
                    :aria-label="t('userManagement.actions.promoteSystemAdmin')"
                    @click.stop="emit('promote-system-admin', row)"
                  >
                    <template #icon><t-icon name="secured" /></template>
                  </t-button>
                </t-tooltip>
              </template>
            </div>
          </template>
        </t-table>
      </div>

      <div class="user-table__pager">
        <t-pagination
          v-model="currentPage"
          v-model:page-size="currentPageSize"
          :total="total"
          size="small"
          :show-jumper="false"
          show-page-number
          :show-page-size="total > PAGE_SIZE_OPTIONS[0]"
          :page-size-options="PAGE_SIZE_OPTIONS"
          @change="emit('change')"
        />
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TenantRole } from '@/api/tenant/members'
import type { UserManagementStatus } from '@/api/user-management'
import {
  gateUserAction,
  roleChangeOptions,
  type ActionGate,
  type DirectoryRole,
  type UserActionKind,
  type UserActionPermissions,
  type UserRow,
} from './types'

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

const props = defineProps<{
  items: UserRow[]
  total: number
  page: number
  pageSize: number
  loading: boolean
  error: string
  permissions: UserActionPermissions
  /** 当前登录用户 ID；用于禁用“操作自己”。 */
  currentUserId: string
  isSystemAdmin: boolean
  /** 当前空间 owner 总数；仅作门控上下文，owner 行的角色下拉已一律禁用。 */
  ownerCount: number
}>()

const emit = defineEmits<{
  'update:page': [page: number]
  'update:pageSize': [pageSize: number]
  change: []
  retry: []
  'change-role': [row: UserRow, role: TenantRole]
  ban: [row: UserRow]
  unban: [row: UserRow]
  purge: [row: UserRow]
  transfer: [row: UserRow]
  'promote-system-admin': [row: UserRow]
  'revoke-system-admin': [row: UserRow]
}>()

const { t, locale } = useI18n()

const currentPage = computed({
  get: () => props.page,
  set: (value: number) => emit('update:page', value),
})

const currentPageSize = computed({
  get: () => props.pageSize,
  set: (value: number) => emit('update:pageSize', value),
})

const columns = computed(() => [
  { colKey: 'email', title: t('userManagement.columns.email'), ellipsis: true, minWidth: 220 },
  {
    colKey: 'username',
    title: t('userManagement.columns.username'),
    ellipsis: true,
    minWidth: 160,
  },
  { colKey: 'role', title: t('userManagement.columns.role'), width: 160 },
  { colKey: 'status', title: t('userManagement.columns.status'), width: 110 },
  {
    colKey: 'created_at',
    title: t('userManagement.columns.createdAt'),
    width: 170,
  },
  { colKey: 'actions', title: t('userManagement.columns.actions'), width: 200 },
])

// 显式列出每个键，方便 i18n 缺键审计静态识别。
const roleLabels = computed<Record<DirectoryRole, string>>(() => ({
  system_admin: t('userManagement.roles.systemAdmin'),
  owner: t('userManagement.roles.owner'),
  admin: t('userManagement.roles.admin'),
  contributor: t('userManagement.roles.contributor'),
  viewer: t('userManagement.roles.viewer'),
}))

const statusLabels = computed<Record<UserManagementStatus, string>>(() => ({
  active: t('userManagement.status.active'),
  banned: t('userManagement.status.banned'),
  deleted: t('userManagement.status.deleted'),
}))

const roleOptions = computed(() =>
  roleChangeOptions(props.isSystemAdmin).map((role) => ({
    label: roleLabels.value[role],
    value: role,
  })),
)

function roleLabel(role: DirectoryRole): string {
  return roleLabels.value[role]
}

function statusLabel(status: UserManagementStatus): string {
  return statusLabels.value[status]
}

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

function roleGate(row: UserRow): ActionGate {
  if (row.role === 'system_admin') return { allowed: false }
  return gate('changeRole', row)
}

function canChangeRole(row: UserRow): boolean {
  return roleGate(row).allowed
}

function statusGate(row: UserRow): ActionGate {
  return gate(row.status === 'banned' ? 'unban' : 'ban', row)
}

function statusActionLabel(row: UserRow): string {
  return row.status === 'banned'
    ? t('userManagement.actions.unban')
    : t('userManagement.actions.ban')
}

function tooltipFor(gateResult: ActionGate, label: string): string {
  return gateResult.reasonKey ? t(gateResult.reasonKey) : label
}

function actionTooltip(kind: UserActionKind, row: UserRow): string {
  if (kind === 'purge') return tooltipFor(gate('purge', row), t('userManagement.actions.purge'))
  if (kind === 'transfer') return tooltipFor(gate('transfer', row), t('userManagement.actions.transfer'))
  return ''
}

function statusTooltip(row: UserRow): string {
  return tooltipFor(statusGate(row), statusActionLabel(row))
}

function promoteTooltip(row: UserRow): string {
  const label = t('userManagement.actions.promoteSystemAdmin')
  if (row.user_id === props.currentUserId) return t('userManagement.tooltips.self')
  return label
}

function revokeTooltip(row: UserRow): string {
  const label = t('userManagement.actions.revokeSystemAdmin')
  if (row.user_id === props.currentUserId) return t('userManagement.tooltips.self')
  return label
}

function emitStatusAction(row: UserRow) {
  if (!statusGate(row).allowed) return
  if (row.status === 'banned') emit('unban', row)
  else emit('ban', row)
}

function onRoleChange(row: UserRow, value: unknown) {
  emit('change-role', row, value as TenantRole)
}

function formatDateTime(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value || 'zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}
</script>

<style scoped lang="less">
.user-table {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  gap: 12px;
}

.user-table__state {
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  min-height: 180px;
  color: var(--td-text-color-secondary);
}

.user-table__scroll {
  flex: 1;
  min-height: 0;
  overflow: auto;
}

.user-table__actions {
  display: flex;
  align-items: center;
  gap: 4px;
}

.user-table__role-text {
  color: var(--td-text-color-primary);
}

.user-table__pager {
  display: flex;
  justify-content: flex-end;
}
</style>
