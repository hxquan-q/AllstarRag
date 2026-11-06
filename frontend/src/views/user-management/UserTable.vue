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

    <div v-else-if="total === 0" class="user-table__state user-table__state--empty">
      <t-empty :description="t('userManagement.states.emptyHint')" />
    </div>

    <template v-else>
      <div class="user-table__scroll">
        <t-table
          row-key="user_id"
          table-layout="fixed"
          :data="items"
          :columns="columns"
          :loading="loading"
          size="medium"
          hover
        >
          <template #account="{ row }">
            <div class="user-table__account">
              <span class="user-table__account-email">{{ row.email || '—' }}</span>
              <span v-if="row.username" class="user-table__account-name">{{ row.username }}</span>
            </div>
          </template>
          <template #role="{ row }">
            <t-tooltip
              v-if="roleGate(row).reasonKey"
              :content="t(roleGate(row).reasonKey ?? '')"
              placement="top"
            >
              <span class="user-table__role-text">{{ roleLabel(row.role) }}</span>
            </t-tooltip>
            <t-select
              v-else-if="canChangeRole(row) && row.role"
              :model-value="selectValue(row)"
              size="small"
              :options="roleOptions"
              :popup-props="{ zIndex: 6200 }"
              @change="(value: unknown) => onRoleChange(row, value)"
            />
            <span v-else class="user-table__role-text">{{ roleLabel(row.role) }}</span>
          </template>
          <template #memberships="{ row }">
            <div v-if="row.memberships?.length" class="user-table__memberships">
              <t-tag
                v-for="membership in visibleMemberships(row)"
                :key="`${membership.tenant_id}-${membership.role}`"
                size="small"
                variant="light"
              >
                {{ membership.tenant_name }} · {{ roleLabel(membership.role) }}
              </t-tag>
              <t-tooltip
                v-if="hiddenMembershipCount(row) > 0"
                :content="hiddenMembershipsText(row)"
                placement="top"
              >
                <t-tag size="small" variant="light">+{{ hiddenMembershipCount(row) }}</t-tag>
              </t-tooltip>
            </div>
            <span v-else class="user-table__role-text">—</span>
          </template>
          <template #status="{ row }">
            <t-tag :theme="statusTheme(row.status)" variant="light">
              {{ statusLabel(row.status) }}
            </t-tag>
          </template>
          <template #created_at="{ row }">{{ formatDateTime(row.created_at) }}</template>
          <template #actions="{ row }">
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
                <t-tooltip :content="statusTooltip(row)" placement="top">
                  <t-button
                    variant="text"
                    size="small"
                    :disabled="!statusGate(row).allowed"
                    @click.stop="emitStatusAction(row)"
                  >
                    {{ statusActionLabel(row) }}
                  </t-button>
                </t-tooltip>

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
                        v-if="row.role === 'owner'"
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
                        :disabled="!resetPasswordAllowed(row)"
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
import type {
  UserManagementListScope,
  UserManagementStatus,
} from '@/api/user-management'
import {
  gateUserAction,
  roleChangeOptions,
  type ActionGate,
  type UserActionKind,
  type UserActionPermissions,
  type UserRow,
} from './types'

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

/** t-table 列描述；ellipsis/宽度都可选。 */
type TableColumn = {
  colKey: string
  title: string
  ellipsis?: boolean
  minWidth?: number
  width?: number
  fixed?: 'left'
}

const props = withDefaults(
  defineProps<{
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
    /** 空间筛选（Q34）：other/all 时显示「所属空间」列。 */
    scope?: UserManagementListScope
    /** 是否显示「重置密码」行操作（owner/系统管理员）；后端仍逐接口校验（C4）。 */
    canResetPassword?: boolean
  }>(),
  { scope: 'current', canResetPassword: false },
)

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
  'reset-password': [row: UserRow]
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

// 角色下拉的展示值：后端 admin 账号归入「编辑」，选值统一落到 contributor。
// 走到这里的角色只会是 viewer/contributor/admin（owner 行显示 tooltip，
// system_admin 行显示文本），兜底取 contributor。
function selectValue(row: UserRow): TenantRole {
  if (row.role === 'viewer') return 'viewer'
  return 'contributor'
}

const columns = computed<TableColumn[]>(() => {
  const list: TableColumn[] = [
    {
      colKey: 'account',
      title: t('userManagement.columns.account'),
      ellipsis: true,
      width: 200,
      fixed: 'left',
    },
  ]
  // 多空间视图才展示「所属空间」；current 视图就是本空间，不重复（AC-26）。
  if (props.scope !== 'current') {
    list.push({
      colKey: 'memberships',
      title: t('userManagement.spaceFilter.spaces'),
      width: 180,
    })
  }
  list.push(
    { colKey: 'role', title: t('userManagement.columns.role'), width: 110 },
    { colKey: 'status', title: t('userManagement.columns.status'), width: 90 },
    {
      colKey: 'created_at',
      title: t('userManagement.columns.createdAt'),
      width: 120,
    },
    { colKey: 'actions', title: t('userManagement.columns.actions'), width: 120 },
  )
  return list
})

// 显式列出每个键，方便 i18n 缺键审计静态识别。
// Q37：contributor 与 admin 在界面上统一显示为「编辑」。
const roleLabels = computed<Record<string, string>>(() => ({
  system_admin: t('userManagement.roles.systemAdmin'),
  owner: t('userManagement.roles.owner'),
  admin: t('userManagement.roles.editor'),
  contributor: t('userManagement.roles.editor'),
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

function roleLabel(role: string): string {
  return role ? (roleLabels.value[role] ?? role) : '—'
}

function statusLabel(status: UserManagementStatus): string {
  return statusLabels.value[status]
}

function statusTheme(status: UserManagementStatus): 'success' | 'danger' | 'warning' {
  if (status === 'active') return 'success'
  if (status === 'banned') return 'danger'
  return 'warning'
}

const MAX_MEMBERSHIP_TAGS = 2

function visibleMemberships(row: UserRow) {
  return (row.memberships ?? []).slice(0, MAX_MEMBERSHIP_TAGS)
}

function hiddenMembershipCount(row: UserRow): number {
  return Math.max(0, (row.memberships?.length ?? 0) - MAX_MEMBERSHIP_TAGS)
}

function hiddenMembershipsText(row: UserRow): string {
  return (row.memberships ?? [])
    .slice(MAX_MEMBERSHIP_TAGS)
    .map((membership) => `${membership.tenant_name} · ${roleLabel(membership.role)}`)
    .join('、')
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
  // 多空间视图里所属空间列已经展示角色；改角色只在本空间视图做，避免看错空间。
  return props.scope === 'current' && roleGate(row).allowed
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

/** 不能重置自己；owner 操作者不能重置其他普通管理员（与后端特权目标保护一致）。 */
function resetPasswordAllowed(row: UserRow): boolean {
  if (row.user_id === props.currentUserId) return false
  if (row.status === 'deleted') return false
  if (row.role === 'owner' && !props.isSystemAdmin) return false
  return true
}

function resetPasswordTooltip(row: UserRow): string {
  if (row.user_id === props.currentUserId) return t('userManagement.tooltips.self')
  if (row.role === 'owner' && !props.isSystemAdmin) return t('userManagement.tooltips.ownerProtected')
  return t('userManagement.resetPassword.action')
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
  overflow: visible;

  // 表头吸顶：整页只有设置弹窗一层滚动，表头在滚动时保持可见。
  :deep(.t-table thead th) {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--td-bg-color-container);
  }
}

.user-table__state--empty {
  color: var(--td-text-color-secondary);
}

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

.user-table__actions {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 2px;
}

.user-table__role-text {
  color: var(--td-text-color-primary);
}

.user-table__memberships {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 4px;
  overflow: hidden;
}

.user-table__pager {
  display: flex;
  justify-content: flex-end;
}
</style>
