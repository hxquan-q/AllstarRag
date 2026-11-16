<template>
  <div class="invite-management">
    <div v-if="loading" class="invite-management__state">
      <t-loading size="small" />
      <span>{{ t('userManagement.states.loading') }}</span>
    </div>

    <div v-else-if="error" class="invite-management__state">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="load">{{ t('userManagement.states.retry') }}</t-button>
        </template>
      </t-alert>
    </div>

    <template v-else>
      <!-- 共享邀请链接：includeTerminal=false，只展示 pending 行。 -->
      <section class="invite-management__section">
        <header class="invite-management__section-header">
          <div>
            <h2 class="invite-management__section-title">{{ t('userManagement.invites.linksTitle') }}</h2>
            <p class="invite-management__section-desc">{{ t('userManagement.invites.linksDesc') }}</p>
          </div>
          <t-button theme="primary" @click="openCreate">{{ t('userManagement.invites.createLink') }}</t-button>
        </header>

        <div v-if="shareLinks.length === 0" class="invite-management__state">
          <t-empty :description="t('userManagement.invites.linksEmpty')" />
        </div>
        <t-table v-else row-key="id" :data="shareLinks" :columns="linkColumns" size="medium" hover>
          <template #token="{ row }"><span>{{ shortToken(row) }}</span></template>
          <template #role="{ row }"><t-tag size="small" variant="light">{{ roleLabel(row.role) }}</t-tag></template>
          <template #usage="{ row }">{{ usageText(row) }}</template>
          <template #expires_at="{ row }">{{ formatDateTime(row.expires_at, locale) }}</template>
          <template #status>
            <t-tag size="small" variant="light" theme="success">{{ t('userManagement.invites.statusActive') }}</t-tag>
          </template>
          <template #actions="{ row }">
            <div class="invite-management__actions">
              <t-tooltip :content="t('userManagement.invites.copyLink')" placement="top">
                <t-button shape="square" variant="text" size="small" :aria-label="t('userManagement.invites.copyLink')" @click="copyLink(row)">
                  <template #icon><t-icon name="copy" /></template>
                </t-button>
              </t-tooltip>
              <t-popconfirm
                theme="warning"
                :content="t('userManagement.invites.revokeLinkConfirm')"
                :confirm-btn="{ content: t('userManagement.invites.revoke'), theme: 'danger' }"
                :cancel-btn="t('userManagement.dialogs.cancel')"
                placement="left"
                @confirm="revoke(row)"
              >
                <t-button theme="danger" shape="square" variant="text" size="small" :aria-label="t('userManagement.invites.revoke')"><template #icon><t-icon name="close" /></template></t-button>
              </t-popconfirm>
            </div>
          </template>
        </t-table>
      </section>

      <!-- 直接邀请：展示 pending/accepted/declined/revoked/expired 全部状态。 -->
      <section class="invite-management__section">
        <header class="invite-management__section-header">
          <div>
            <h2 class="invite-management__section-title">{{ t('userManagement.invites.invitesTitle') }}</h2>
            <p class="invite-management__section-desc">{{ t('userManagement.invites.invitesDesc') }}</p>
          </div>
        </header>

        <div v-if="directInvites.length === 0" class="invite-management__state">
          <t-empty :description="t('userManagement.invites.invitesEmpty')" />
        </div>
        <t-table v-else row-key="id" :data="directInvites" :columns="inviteColumns" size="medium" hover>
          <template #invitee="{ row }">
            <div class="invite-management__invitee">
              <span>{{ inviteeName(row) }}</span>
              <span v-if="row.invitee_email" class="invite-management__muted">{{ row.invitee_email }}</span>
            </div>
          </template>
          <template #role="{ row }"><t-tag size="small" variant="light">{{ roleLabel(row.role) }}</t-tag></template>
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="statusTheme(row.status)">{{ statusLabel(row.status) }}</t-tag>
          </template>
          <template #expires_at="{ row }">{{ formatDateTime(row.expires_at, locale) }}</template>
          <template #actions="{ row }">
            <t-popconfirm
              v-if="row.status === 'pending'"
              theme="warning"
              :content="t('userManagement.invites.revokeInviteConfirm', { email: row.invitee_email || row.invitee_user_id })"
              :confirm-btn="{ content: t('userManagement.invites.revoke'), theme: 'danger' }"
              :cancel-btn="t('userManagement.dialogs.cancel')"
              placement="left"
              @confirm="revoke(row)"
            >
              <t-button theme="danger" shape="square" variant="text" size="small" :aria-label="t('userManagement.invites.revoke')"><template #icon><t-icon name="close" /></template></t-button>
            </t-popconfirm>
            <span v-else class="invite-management__muted">—</span>
          </template>
        </t-table>
      </section>
    </template>

    <t-dialog
      :visible="createVisible"
      width="480px"
      :header="t('userManagement.invites.createTitle')"
      :confirm-btn="{ content: t('userManagement.invites.submit'), loading: creating }"
      :cancel-btn="{ content: t('userManagement.dialogs.cancel') }"
      :close-on-overlay-click="!creating" :close-on-esc-keydown="!creating"
      @update:visible="onCreateVisibleUpdate" @confirm="submitCreate"
    >
      <p class="invite-management__create-desc">{{ t('userManagement.invites.createDesc') }}</p>
      <t-form :data="createForm" label-align="top" @submit.prevent>
        <t-form-item :label="t('userManagement.invites.role')" name="role">
          <t-select v-model="createForm.role" :options="roleOptions" :popup-props="{ zIndex: 6200 }" />
        </t-form-item>
        <t-form-item :label="t('userManagement.invites.expiry')" name="expiryDays">
          <t-select v-model="createForm.expiryDays" :options="expiryOptions" :popup-props="{ zIndex: 6200 }" />
        </t-form-item>
        <t-form-item :label="t('userManagement.invites.maxRegistrations')" name="maxRegistrations">
          <t-input-number
            :model-value="createForm.maxRegistrations ?? undefined"
            :min="1" :max="1000" :step="1" :decimal-places="0"
            :placeholder="t('userManagement.invites.maxRegistrationsPlaceholder')"
            @change="onMaxChange"
          />
        </t-form-item>
      </t-form>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import type { TenantRole } from '@/api/tenant/members'
import {
  createInviteLink,
  listTenantInvitations,
  revokeInvitation,
  type CreateInviteLinkRequest,
  type TenantInvitation,
} from '@/api/tenant/invitations'
import { resolveUserManagementError } from '@/api/user-management'
import { copyWithToast } from '@/utils/clipboard'
import { absoluteInviteURL, formatDateTime, inviteeName, shortToken } from './inviteHelpers'

/** 邀请列表分页兜底：单页 50 条，最多拉 10 页。 */
const LIST_PAGE_SIZE = 50
const LIST_MAX_PAGES = 10

const props = defineProps<{
  tenantId: number | null
}>()

const { t, locale } = useI18n()

const loading = ref(false)
const error = ref('')
const shareLinks = ref<TenantInvitation[]>([])
const directInvites = ref<TenantInvitation[]>([])

/** 逐页拉完邀请列表；total 不可信时由页数上限兜底。 */
async function fetchAllInvitations(tenantId: number, includeTerminal: boolean): Promise<TenantInvitation[]> {
  const collected: TenantInvitation[] = []
  let reportedTotal = 0
  for (let pageNo = 1; pageNo <= LIST_MAX_PAGES; pageNo++) {
    const response = await listTenantInvitations(tenantId, { includeTerminal, page: pageNo, page_size: LIST_PAGE_SIZE })
    if (!response.success || !response.data) throw new Error(response.message || 'list invitations failed')
    const rows = response.data.invitations ?? []
    collected.push(...rows)
    reportedTotal = response.data.total ?? 0
    if (rows.length < LIST_PAGE_SIZE || collected.length >= reportedTotal) break
  }
  return collected
}

// 组件卸载后迟到的请求不得再写状态。
let disposed = false

async function load() {
  if (props.tenantId == null) return
  loading.value = true
  error.value = ''
  try {
    // 链接区只看 pending（includeTerminal=false）；直接邀请区要看全部状态。
    const [pending, all] = await Promise.all([
      fetchAllInvitations(props.tenantId, false),
      fetchAllInvitations(props.tenantId, true),
    ])
    if (disposed) return
    shareLinks.value = pending.filter((row) => row.is_share_link)
    directInvites.value = all.filter((row) => !row.is_share_link)
  } catch (loadError) {
    if (disposed) return
    error.value = resolveUserManagementError(loadError)
    shareLinks.value = []
    directInvites.value = []
  } finally {
    if (!disposed) loading.value = false
  }
}

onMounted(() => void load())
onBeforeUnmount(() => {
  disposed = true
})

// KeepAlive 切走再切回时组件不重建，onMounted 不会重跑；首次激活与
// onMounted 重叠，用标记跳过，避免首屏双轮请求。
let activatedOnce = false
onActivated(() => {
  if (!activatedOnce) {
    activatedOnce = true
    return
  }
  void load()
})

// Tab 停留期间切换空间时重新加载。
watch(() => props.tenantId, () => void load())

type TableColumn = { colKey: string; title: string; ellipsis?: boolean; width?: number; minWidth?: number }

const linkColumns = computed<TableColumn[]>(() => [
  { colKey: 'token', title: t('userManagement.invites.token'), width: 140 },
  { colKey: 'role', title: t('userManagement.columns.role'), width: 130 },
  { colKey: 'usage', title: t('userManagement.invites.usage'), width: 140 },
  { colKey: 'expires_at', title: t('userManagement.invites.expiresAt'), width: 180 },
  { colKey: 'status', title: t('userManagement.columns.status'), width: 110 },
  { colKey: 'actions', title: t('userManagement.columns.actions'), width: 120 },
])

const inviteColumns = computed<TableColumn[]>(() => [
  { colKey: 'invitee', title: t('userManagement.invites.invitee'), minWidth: 220, ellipsis: true },
  { colKey: 'role', title: t('userManagement.columns.role'), width: 130 },
  { colKey: 'status', title: t('userManagement.columns.status'), width: 120 },
  { colKey: 'expires_at', title: t('userManagement.invites.expiresAt'), width: 180 },
  { colKey: 'actions', title: t('userManagement.columns.actions'), width: 100 },
])

// 残余 contributor（迁移前发出的邀请）按普通管理员归并显示，口径同 userTableLabels。
const roleLabels = computed<Record<string, string>>(() => ({
  owner: t('userManagement.roles.owner'),
  admin: t('userManagement.roles.admin'),
  contributor: t('userManagement.roles.admin'),
  viewer: t('userManagement.roles.viewer'),
}))

const roleLabel = (role: string): string => (role ? (roleLabels.value[role] ?? role) : '—')

type TagTheme = 'default' | 'primary' | 'success' | 'warning' | 'danger'

const statusLabels = computed<Record<string, string>>(() => ({
  pending: t('userManagement.invites.statusPending'),
  accepted: t('userManagement.invites.statusAccepted'),
  declined: t('userManagement.invites.statusDeclined'),
  revoked: t('userManagement.invites.statusRevoked'),
  expired: t('userManagement.invites.statusExpired'),
}))

const STATUS_THEMES: Record<string, TagTheme> = {
  pending: 'warning',
  accepted: 'success',
  declined: 'default',
  revoked: 'default',
  expired: 'default',
}

const statusLabel = (status: string): string => statusLabels.value[status] ?? status
const statusTheme = (status: string): TagTheme => STATUS_THEMES[status] ?? 'default'

function usageText(row: TenantInvitation): string {
  const registered = row.registered_count ?? 0
  if (row.max_registrations != null) {
    return t('userManagement.invites.usageWithLimit', { count: registered, limit: row.max_registrations })
  }
  return t('userManagement.invites.usageCount', { count: registered })
}

async function copyLink(row: TenantInvitation) {
  await copyWithToast(absoluteInviteURL(row.invite_url || ''), 'userManagement.invites.copied')
}

const revoking = ref(false)

async function revoke(row: TenantInvitation) {
  if (props.tenantId == null || revoking.value) return
  revoking.value = true
  try {
    const response = await revokeInvitation(props.tenantId, row.id)
    if (!response.success) throw new Error(response.message || 'revoke invitation failed')
    MessagePlugin.success(t('userManagement.invites.revoked'))
    await load()
  } catch (revokeError) {
    MessagePlugin.error(resolveUserManagementError(revokeError))
  } finally {
    revoking.value = false
  }
}

type ExpiryDays = '7' | '30' | '90'

const createVisible = ref(false)
const creating = ref(false)
const createForm = reactive<{ role: TenantRole; expiryDays: ExpiryDays; maxRegistrations: number | null }>({
  role: 'admin',
  expiryDays: '7',
  maxRegistrations: null,
})

// T09：链接角色只提供 普通管理员/游客；owner/contributor 停止分配（C10）。
const roleOptions = computed<Array<{ label: string; value: TenantRole }>>(() => [
  { label: t('userManagement.roles.admin'), value: 'admin' },
  { label: t('userManagement.roles.viewer'), value: 'viewer' },
])

const expiryOptions = computed(() => [
  { label: t('userManagement.invites.expiry7'), value: '7' },
  { label: t('userManagement.invites.expiry30'), value: '30' },
  { label: t('userManagement.invites.expiry90'), value: '90' },
])

function openCreate() {
  createForm.role = 'admin'
  createForm.expiryDays = '7'
  createForm.maxRegistrations = null
  createVisible.value = true
}
function onCreateVisibleUpdate(visible: boolean) {
  if (creating.value) return
  createVisible.value = visible
}

/** 人数输入清空时控件给空值，统一折算为「不限」。 */
function onMaxChange(value: number | string | undefined) {
  createForm.maxRegistrations = value === '' || value == null ? null : Number(value)
}

/**
 * 邀请创建失败的文案。409 在邀请链路上可能是「目标账号已属于其他空间」
 * （一号一空间），优先给专属原因；其余状态码仍走统一映射。
 */
function inviteCreateErrorText(error: unknown): string {
  const status = (error as { status?: number } | null)?.status
  const message = (error as { message?: string } | null)?.message ?? ''
  if (status === 409 && /belongs to another workspace/i.test(message)) {
    return t('userManagement.errors.userAlreadyInSpace')
  }
  return resolveUserManagementError(error)
}

async function submitCreate() {
  if (creating.value || props.tenantId == null) return
  const max = createForm.maxRegistrations
  if (max !== null && (!Number.isInteger(max) || max < 1 || max > 1000)) {
    MessagePlugin.error(t('userManagement.invites.invalidMaxRegistrations'))
    return
  }
  creating.value = true
  try {
    const body: CreateInviteLinkRequest = {
      role: createForm.role,
      // 预设按天累加（默认 7 天），接口要求 RFC3339 的未来时间。
      expires_at: new Date(Date.now() + Number(createForm.expiryDays) * 24 * 60 * 60 * 1000).toISOString(),
    }
    if (max !== null) body.max_registrations = max
    const response = await createInviteLink(props.tenantId, body)
    if (!response.success) throw new Error(response.message || 'create invite link failed')
    MessagePlugin.success(t('userManagement.invites.created'))
    createVisible.value = false
    await load()
  } catch (createError) {
    MessagePlugin.error(inviteCreateErrorText(createError))
  } finally {
    creating.value = false
  }
}
</script>

<style scoped lang="less">
.invite-management {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 20px;
  min-height: 0;
  overflow: auto;
}

.invite-management__state {
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  min-height: 180px;
  color: var(--td-text-color-secondary);
}

.invite-management__section { display: flex; flex: 0 0 auto; flex-direction: column; gap: 12px; }

.invite-management__section-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }

.invite-management__section-title { margin: 0; font-size: var(--app-text-xl); font-weight: 600; line-height: 24px; color: var(--td-text-color-primary); }

.invite-management__section-desc,
.invite-management__create-desc {
  margin: 2px 0 0;
  font-size: var(--app-text-base);
  line-height: 22px;
  color: var(--td-text-color-secondary);
}

.invite-management__create-desc { margin: 0 0 12px; }

.invite-management__actions { display: flex; align-items: center; gap: 4px; }

.invite-management__invitee { display: flex; flex-direction: column; gap: 2px; }

.invite-management__muted { font-size: var(--app-text-sm); color: var(--td-text-color-secondary); }
</style>
