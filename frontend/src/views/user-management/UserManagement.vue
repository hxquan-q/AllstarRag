<template>
  <div class="user-management">
    <header class="user-management__header">
      <h1 class="user-management__title">{{ t('userManagement.title') }}</h1>
      <p class="user-management__subtitle">{{ t('userManagement.subtitle') }}</p>
    </header>

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

    <div v-else class="user-management__body">
      <RoleDirectory
        :title="t('userManagement.directory.title')"
        :entries="directoryEntries"
        :selected="selectedRole"
        @select="selectRole"
      />

      <section class="user-management__content">
        <div class="user-management__toolbar">
          <t-input
            v-model="searchQuery"
            class="user-management__search"
            :placeholder="t('userManagement.searchPlaceholder')"
            clearable
          >
            <template #prefix-icon><t-icon name="search" size="16px" /></template>
          </t-input>

          <t-space v-if="canManageOwners || canAddUser" size="8px">
            <t-button v-if="canAddUser" theme="primary" @click="addUserVisible = true">
              {{ t('userManagement.actions.addUser') }}
            </t-button>
            <template v-if="canManageOwners">
              <template v-if="isSystemAdmin">
                <t-button variant="outline" @click="openAddOwner('provision')">
                  {{ t('userManagement.actions.provisionOwnerSpace') }}
                </t-button>
                <t-button variant="outline" @click="openAddOwner('inviteOwner')">
                  {{ t('userManagement.actions.inviteOwner') }}
                </t-button>
              </template>
              <template v-else>
                <t-button variant="outline" @click="openAddOwner('createCoOwner')">
                  {{ t('userManagement.actions.createCoOwner') }}
                </t-button>
                <t-button variant="outline" @click="openAddOwner('inviteCoOwner')">
                  {{ t('userManagement.actions.inviteCoOwner') }}
                </t-button>
              </template>
            </template>
          </t-space>
        </div>

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
          @change="onPageChange"
          @retry="load"
          @change-role="onChangeRole"
          @ban="openSimpleAction('ban', $event)"
          @unban="openSimpleAction('unban', $event)"
          @purge="openSimpleAction('purge', $event)"
          @transfer="openTransfer"
          @promote-system-admin="onPromoteSystemAdmin"
          @revoke-system-admin="onRevokeSystemAdmin"
        />
      </section>
    </div>

    <UserActionDialogs
      :tenant-id="tenantId"
      :target="actionTarget"
      :mode="actionMode"
      :candidates="transferCandidates"
      @close="closeAction"
      @completed="onActionCompleted"
    />

    <AddOwnerDialog
      v-model:visible="addOwnerVisible"
      :tenant-id="tenantId"
      :mode="addOwnerMode"
      @completed="reload"
    />

    <AddUserDialog
      v-model:visible="addUserVisible"
      :tenant-id="tenantId"
      :actor-role="actorRole"
      :is-system-admin="isSystemAdmin"
      :can-create="canCreateUser"
      :can-invite="canInviteUser"
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
import AddOwnerDialog from './AddOwnerDialog.vue'
import AddUserDialog from './AddUserDialog.vue'
import { useUserManagement } from './useUserManagement'
import type {
  AddOwnerMode,
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

const actionMode = ref<UserActionMode | null>(null)
const actionTarget = ref<UserRow | null>(null)
const transferCandidates = ref<UserRow[]>([])

const addOwnerVisible = ref(false)
const addOwnerMode = ref<AddOwnerMode>('createCoOwner')
const addUserVisible = ref(false)

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

function openAddOwner(mode: AddOwnerMode) {
  addOwnerMode.value = mode
  addOwnerVisible.value = true
}
</script>

<style scoped lang="less">
.user-management {
  display: flex;
  flex-direction: column;
  gap: 16px;
  height: 100%;
  min-height: 0;
  padding: 24px;
  overflow: hidden;
  background: var(--td-bg-color-container);
}

.user-management__header {
  flex: 0 0 auto;
}

.user-management__title {
  margin: 0;
  font-size: var(--app-text-3xl);
  font-weight: 600;
  line-height: 28px;
  color: var(--td-text-color-primary);
}

.user-management__subtitle {
  margin: 4px 0 0;
  font-size: var(--app-text-base);
  line-height: 22px;
  color: var(--td-text-color-secondary);
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
  gap: 20px;
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

.user-management__toolbar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.user-management__search {
  width: 320px;
  max-width: 100%;
}
</style>
