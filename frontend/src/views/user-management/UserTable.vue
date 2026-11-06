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
            <UserAccountCell :row="row" />
          </template>
          <template #role="{ row }">
            <t-tooltip
              v-if="roleGate(row).reasonKey"
              :content="t(roleGate(row).reasonKey ?? '')"
              placement="top"
            >
              <span class="user-table__role-text">{{ roleLabelOf(row.role) }}</span>
            </t-tooltip>
            <t-select
              v-else-if="canChangeRole(row) && row.role"
              :model-value="selectValue(row)"
              size="small"
              :options="roleOptions"
              :popup-props="{ zIndex: 6200 }"
              @change="(value: unknown) => onRoleChange(row, value)"
            />
            <span v-else class="user-table__role-text">{{ roleLabelOf(row.role) }}</span>
          </template>
          <template #memberships="{ row }">
            <UserMembershipsCell :row="row" :role-label="(role) => roleLabelOf(role)" />
          </template>
          <template #status="{ row }">
            <UserStatusCell :label="statusLabelOf(row.status)" :theme="statusTheme(row.status)" />
          </template>
          <template #created_at="{ row }">{{ formatCreatedAt(row.created_at) }}</template>
          <template #actions="{ row }">
            <UserActionsCell
              :row="row"
              :current-user-id="currentUserId"
              :is-system-admin="isSystemAdmin"
              :permissions="permissions"
              :owner-count="ownerCount"
              :can-reset-password="canResetPassword"
              :t="t"
              @ban="emit('ban', $event)"
              @unban="emit('unban', $event)"
              @purge="emit('purge', $event)"
              @transfer="emit('transfer', $event)"
              @promote-system-admin="emit('promote-system-admin', $event)"
              @revoke-system-admin="emit('revoke-system-admin', $event)"
              @reset-password="emit('reset-password', $event)"
            />
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
import type { UserManagementListScope } from '@/api/user-management'
import {
  gateUserAction,
  roleChangeOptions,
  type ActionGate,
  type UserActionPermissions,
  type UserRow,
} from './types'
import UserAccountCell from './UserAccountCell.vue'
import UserMembershipsCell from './UserMembershipsCell.vue'
import UserStatusCell from './UserStatusCell.vue'
import UserActionsCell from './UserActionsCell.vue'
import { formatDateTime, roleLabel, statusLabel, statusTheme } from './userTableLabels'

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
const roleLabelOf = (role: string) => roleLabel(role, t)
const statusLabelOf = (status: UserRow['status']) => statusLabel(status, t)
const formatCreatedAt = (value: string) => formatDateTime(value, locale.value)

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

const roleOptions = computed(() =>
  roleChangeOptions(props.isSystemAdmin).map((role) => ({
    label: roleLabelOf(role),
    value: role,
  })),
)

function gate(kind: 'changeRole', row: UserRow): ActionGate {
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

function onRoleChange(row: UserRow, value: unknown) {
  emit('change-role', row, value as TenantRole)
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

.user-table__role-text {
  color: var(--td-text-color-primary);
}

.user-table__pager {
  display: flex;
  justify-content: flex-end;
}
</style>
