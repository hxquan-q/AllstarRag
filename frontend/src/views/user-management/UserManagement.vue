<template>
  <div class="user-management">
    <div v-if="capabilitiesPending" class="user-management__state">
      <t-loading size="small" />
      <span>{{ t('userManagement.states.loading') }}</span>
    </div>

    <!-- 无权限时不渲染页面内容，也不显示空表；真正权限仍由后端校验（C4）。 -->
    <t-alert
      v-else-if="!canView"
      theme="warning"
      :message="t('userManagement.states.noPermission')"
    />

    <template v-else>
      <!-- 顶部一行：Tab 在左，搜索与「新增账号」在右，尽量把首行数据往上提。 -->
      <div class="user-management__topbar">
        <t-tabs v-model="activeTab" class="user-management__tabs">
          <t-tab-panel value="members" :label="t('userManagement.invites.tabMembers')" />
          <t-tab-panel v-if="canSeeInvites" value="invites" :label="t('userManagement.invites.tabInvites')" />
          <t-tab-panel v-if="canSeeOperations" value="operations" :label="t('userManagement.operations.tab')" />
        </t-tabs>

        <div v-show="activeTab === 'members'" class="user-management__toolbar">
          <t-input
            v-model="searchQuery"
            class="user-management__search"
            :placeholder="t('userManagement.searchPlaceholder')"
            clearable
          >
            <template #prefix-icon><t-icon name="search" size="16px" /></template>
          </t-input>

          <!-- Q38：只保留一个「新增账号」入口；普通管理员与超管专属操作都在弹窗内。 -->
          <t-button v-if="canAddUser || isSystemAdmin" theme="primary" @click="addUserVisible = true">
            {{ t('userManagement.actions.addUser') }}
          </t-button>
        </div>
      </div>

      <div v-show="activeTab === 'members'" class="user-management__body">
      <RoleDirectory
        :title="t('userManagement.directory.title')"
        :entries="directoryEntries"
        :selected="selectedRole"
        :show-scope-filter="isSystemAdmin"
        :scope="scope"
        @select="selectRole"
        @scope-change="selectScope"
      />

      <section class="user-management__content">
        <UserTable
          v-model:page="page"
          v-model:page-size="pageSize"
          :items="rows"
          :total="total"
          :loading="loading"
          :error="error"
          :permissions="permissions"
          :current-user-id="currentUserId"
          :is-system-admin="isSystemAdmin"
          :owner-count="ownerCount"
          :scope="scope"
          :can-reset-password="canResetPassword"
          @change="onPageChange"
          @retry="load"
          @change-role="onChangeRole"
          @ban="openSimpleAction('ban', $event)"
          @unban="openSimpleAction('unban', $event)"
          @purge="openSimpleAction('purge', $event)"
          @transfer="openTransfer"
          @promote-system-admin="onPromoteSystemAdmin"
          @revoke-system-admin="onRevokeSystemAdmin"
          @reset-password="resetTarget = $event"
        />
      </section>
      </div>

      <!-- 邀请管理 Tab：v-if 挂载时自取数据；组件内部负责加载/空/错误态。 -->
      <InviteManagement
        v-if="canSeeInvites && activeTab === 'invites'"
        :tenant-id="tenantId"
        :can-invite-owner="canManageOwners"
      />

      <!-- 操作记录 Tab：只读本空间审计（后端按 tenant_id 过滤，C4）。 -->
      <OperationLog
        v-if="canSeeOperations && activeTab === 'operations'"
        :tenant-id="tenantId"
      />
    </template>

    <UserActionDialogs
      :tenant-id="tenantId"
      :target="actionTarget"
      :mode="actionMode"
      :candidates="transferCandidates"
      @close="closeAction"
      @completed="onActionCompleted"
    />

    <AddUserDialog
      v-model:visible="addUserVisible"
      :tenant-id="tenantId"
      :actor-role="actorRole"
      :is-system-admin="isSystemAdmin"
      :can-create="canCreateUser"
      :can-invite="canInviteUser"
      :can-add-owner="canManageOwners"
      @completed="reload"
    />

    <ResetPasswordDialog
      :target="resetTarget"
      :tenant-id="tenantId"
      :use-platform-api="isSystemAdmin && scope !== 'current'"
      @close="resetTarget = null"
      @completed="reload"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { updateMemberRole, type TenantRole } from '@/api/tenant/members'
import { promoteUserToSystemAdmin, revokeSystemAdmin } from '@/api/system'
import { resolveUserManagementError } from '@/api/user-management'
import RoleDirectory from './RoleDirectory.vue'
import UserTable from './UserTable.vue'
import UserActionDialogs from './UserActionDialogs.vue'
import AddUserDialog from './AddUserDialog.vue'
import ResetPasswordDialog from './ResetPasswordDialog.vue'
import InviteManagement from './InviteManagement.vue'
import OperationLog from './OperationLog.vue'
import { useUserManagement } from './useUserManagement'
import type {
  UserActionMode,
  UserActionPermissions,
  UserRow,
} from './types'

const { t } = useI18n()

const {
  can,
  canView,
  capabilitiesPending,
  currentUserId,
  isSystemAdmin,
  actorRole,
  ownerCount,
  directoryEntries,
  selectedRole,
  scope,
  selectScope,
  searchQuery,
  rows,
  total,
  page,
  pageSize,
  loading,
  error,
  tenantId,
  load,
  reload,
  loadTransferCandidates,
  selectRole,
  onPageChange,
} = useUserManagement()

const permissions = computed<UserActionPermissions>(() => ({
  changeRole: can('user.role.change'),
  ban: can('user.ban'),
  unban: can('user.unban'),
  purge: can('user.purge'),
  transfer: can('space.transfer'),
}))

const canManageOwners = computed(() => can('user.owner.add'))
const canCreateUser = computed(() => can('user.create'))
const canInviteUser = computed(() => can('user.invite'))
const canAddUser = computed(() => canCreateUser.value || canInviteUser.value)

// 邀请管理 Tab 仅对有邀请能力的角色（owner/系统管理员）可见；
// 前端只做入口门控，接口本身仍由后端逐项校验（C4）。
const canSeeInvites = computed(() => can('user.invite'))
// 操作记录 Tab 与用户管理同门槛（owner/系统管理员）；数据仍由后端按空间过滤。
const canSeeOperations = computed(() => can('user.view'))
type ManagementTab = 'members' | 'invites' | 'operations'
const activeTab = ref<ManagementTab>('members')

const actionMode = ref<UserActionMode | null>(null)
const actionTarget = ref<UserRow | null>(null)
const transferCandidates = ref<UserRow[]>([])

const addUserVisible = ref(false)
// 重置密码：owner/系统管理员可见（后端 reset-password 为 Admin+/系统管理员，C4）。
const canResetPassword = computed(() => can('user.view'))
const resetTarget = ref<UserRow | null>(null)

function openSimpleAction(mode: UserActionMode, row: UserRow) {
  actionTarget.value = row
  actionMode.value = mode
}

async function openTransfer(row: UserRow) {
  try {
    transferCandidates.value = await loadTransferCandidates()
    openSimpleAction('transfer', row)
  } catch (loadError) {
    MessagePlugin.error(resolveUserManagementError(loadError))
  }
}

function closeAction() {
  actionMode.value = null
  actionTarget.value = null
}

function onActionCompleted() {
  void reload()
}

async function onChangeRole(row: UserRow, role: TenantRole) {
  if (tenantId.value == null) return
  // owner 行的角色下拉在 UserTable 已禁用（后端要求先交接）；
  // 这里不再“先提示再发请求”，避免必然 403 的无效调用。
  try {
    const response = await updateMemberRole(tenantId.value, row.user_id, role)
    if (!response.success) {
      throw new Error(response.message || 'role change failed')
    }
    MessagePlugin.success(t('userManagement.toasts.roleChanged'))
    await reload()
  } catch (changeError) {
    MessagePlugin.error(resolveUserManagementError(changeError))
  }
}

async function onPromoteSystemAdmin(row: UserRow) {
  try {
    await promoteUserToSystemAdmin({ user_id: row.user_id })
    MessagePlugin.success(t('userManagement.toasts.systemAdminPromoted'))
    await reload()
  } catch (promoteError) {
    MessagePlugin.error(resolveUserManagementError(promoteError))
  }
}

async function onRevokeSystemAdmin(row: UserRow) {
  try {
    await revokeSystemAdmin(row.user_id)
    MessagePlugin.success(t('userManagement.toasts.systemAdminRevoked'))
    await reload()
  } catch (revokeError) {
    MessagePlugin.error(resolveUserManagementError(revokeError))
  }
}
</script>

<style scoped lang="less">
.user-management {
  display: flex;
  flex-direction: column;
  gap: 12px;
  height: 100%;
  min-height: 0;
  padding: 16px 20px;
  overflow-y: auto;
  background: var(--td-bg-color-container);
}

.user-management__topbar {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px 16px;
}

.user-management__toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-left: auto;
}

.user-management__search {
  width: 260px;
  max-width: 100%;
}

.user-management__state {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: var(--td-text-color-secondary);
}

.user-management__body {
  display: flex;
  flex: 1;
  gap: 16px;
  min-height: 0;
}

.user-management__content {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
  min-height: 0;
}

.user-management__tabs {
  flex: 1;
  min-width: 0;

  // 面板内容都挂在 tabs 之外自管显隐，隐藏 TDesign 的空内容区。
  :deep(.t-tabs__content) {
    display: none;
  }
}
</style>
