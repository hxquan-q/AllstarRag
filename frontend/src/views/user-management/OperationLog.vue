<template>
  <div class="operation-log">
    <div class="operation-log__toolbar">
      <t-select
        v-model="actionFilter"
        class="operation-log__filter"
        clearable
        :options="actionOptions"
        :placeholder="t('userManagement.operations.allActions')"
        :popup-props="{ zIndex: 6200 }"
      />
      <t-input
        v-model="keyword"
        class="operation-log__search"
        clearable
        :placeholder="t('userManagement.operations.searchPlaceholder')"
      >
        <template #prefix-icon><t-icon name="search" size="16px" /></template>
      </t-input>
      <t-button variant="outline" :loading="loading" @click="load(true)">
        {{ t('userManagement.operations.refresh') }}
      </t-button>
    </div>

    <div v-if="error" class="operation-log__state">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="load(true)">{{ t('userManagement.states.retry') }}</t-button>
        </template>
      </t-alert>
    </div>

    <div v-else-if="loading && entries.length === 0" class="operation-log__state">
      <t-loading size="small" />
      <span>{{ t('userManagement.states.loading') }}</span>
    </div>

    <div v-else-if="visibleRows.length === 0" class="operation-log__state">
      <t-empty :description="t('userManagement.operations.empty')" />
    </div>

    <template v-else>
      <t-table row-key="id" :data="visibleRows" :columns="columns" size="medium" hover>
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #actor="{ row }">{{ displayUser(row.actor_user_id) }}</template>
        <template #action="{ row }">
          <t-tag size="small" variant="light">{{ actionLabel(row.action) }}</t-tag>
        </template>
        <template #target="{ row }">
          <div class="operation-log__target">
            <span>{{ displayUser(row.target_user_id) }}</span>
            <span v-if="roleDiff(row)" class="operation-log__muted">{{ roleDiff(row) }}</span>
          </div>
        </template>
        <template #outcome="{ row }">
          <t-tag size="small" variant="light" :theme="outcomeTheme(row.outcome)">
            {{ outcomeLabel(row.outcome) }}
          </t-tag>
        </template>
      </t-table>
      <div v-if="hasMore" class="operation-log__more">
        <t-button variant="text" :loading="loading" @click="load(false)">
          {{ t('userManagement.operations.loadMore') }}
        </t-button>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { listAuditLog, type AuditLog, type AuditOutcome } from '@/api/tenant/audit-log'
import { listUsers, resolveUserManagementError } from '@/api/user-management'
import { auditActionLabel } from '@/i18n/auditActionLabel'
import { AUDIT_ACTION_I18N_ROOTS } from '@/i18n/auditActionRegistry'

const props = defineProps<{ tenantId: number | null | undefined }>()

const { t, tm, locale } = useI18n()

const PAGE_SIZE = 50
// 只展示成员管理相关动作；access_denied 等噪音不进入 owner 的操作记录。
const MEMBER_ACTIONS = [
  'rbac.member_added',
  'rbac.member_removed',
  'rbac.member_role_changed',
  'rbac.member_password_reset',
  'rbac.invitation_sent',
  'rbac.invitation_accepted',
  'rbac.invitation_revoked',
  'rbac.member_banned',
  'rbac.member_unbanned',
  'rbac.member_purged',
  'rbac.owner_transferred',
] as const

const entries = ref<AuditLog[]>([])
const cursor = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')
const actionFilter = ref<string>('')
const keyword = ref('')
const userNames = ref<Record<string, string>>({})

const actionLabel = (action: string) =>
  auditActionLabel({ tm }, AUDIT_ACTION_I18N_ROOTS.tenantMember, action)

const actionOptions = computed(() =>
  MEMBER_ACTIONS.map((value) => ({ value, label: actionLabel(value) })),
)

const columns = computed(() => [
  { colKey: 'created_at', title: t('userManagement.operations.columns.time'), width: 180 },
  { colKey: 'actor', title: t('userManagement.operations.columns.actor'), ellipsis: true },
  { colKey: 'action', title: t('userManagement.operations.columns.action'), width: 160 },
  { colKey: 'target', title: t('userManagement.operations.columns.target'), ellipsis: true },
  { colKey: 'outcome', title: t('userManagement.operations.columns.outcome'), width: 100 },
])

// 后端只按 action 精确过滤；关键词在已加载的行上本地匹配操作人/目标。
const visibleRows = computed(() => {
  const memberRows = entries.value.filter((row) =>
    (MEMBER_ACTIONS as readonly string[]).includes(row.action),
  )
  const q = keyword.value.trim().toLowerCase()
  if (!q) return memberRows
  return memberRows.filter((row) =>
    `${displayUser(row.actor_user_id)} ${displayUser(row.target_user_id)}`.toLowerCase().includes(q),
  )
})

function displayUser(userId: string): string {
  if (!userId) return '—'
  return userNames.value[userId] || t('userManagement.operations.unknownUser')
}

function roleDiff(row: AuditLog): string {
  if (row.action !== 'rbac.member_role_changed') return ''
  const details = typeof row.details === 'object' && row.details ? row.details : null
  const from = details?.old_role
  const to = details?.new_role
  if (typeof from !== 'string' || typeof to !== 'string') return ''
  return `${roleLabel(from)} → ${roleLabel(to)}`
}

function roleLabel(role: string): string {
  if (role === 'owner') return t('userManagement.directory.owner')
  if (role === 'viewer') return t('userManagement.directory.viewer')
  return t('userManagement.directory.editor')
}

function outcomeTheme(outcome: AuditOutcome): 'success' | 'danger' | 'default' {
  if (outcome === 'success') return 'success'
  if (outcome === 'denied' || outcome === 'failed') return 'danger'
  return 'default'
}

function outcomeLabel(outcome: AuditOutcome): string {
  if (outcome === 'success') return t('userManagement.operations.outcomeSuccess')
  if (outcome === 'denied' || outcome === 'failed') return t('userManagement.operations.outcomeFailed')
  return outcome
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value)
}

// 操作人/目标只回传 user_id；用本空间成员列表回填邮箱，查不到的显示「未知用户」。
async function loadUserNames(tenantId: number) {
  try {
    const resp = await listUsers(tenantId, { page: 1, page_size: 100 })
    const map: Record<string, string> = {}
    for (const item of resp.data?.items ?? []) {
      map[item.user_id] = item.email || item.username || item.user_id
    }
    userNames.value = map
  } catch {
    // 回填失败不影响记录本身展示。
  }
}

async function load(reset: boolean) {
  const tenantId = props.tenantId
  if (!tenantId || loading.value) return
  loading.value = true
  error.value = ''
  try {
    const resp = await listAuditLog(tenantId, {
      after_id: reset ? undefined : cursor.value || undefined,
      limit: PAGE_SIZE,
      action: actionFilter.value || undefined,
    })
    if (!resp.success) throw new Error(resp.message || '')
    const rows = resp.data ?? []
    entries.value = reset ? rows : [...entries.value, ...rows]
    cursor.value = resp.next_cursor ?? 0
    hasMore.value = Boolean(resp.next_cursor)
  } catch (err) {
    error.value = resolveUserManagementError(err)
  } finally {
    loading.value = false
  }
}

watch(actionFilter, () => load(true))
watch(
  () => props.tenantId,
  (id) => {
    if (!id) return
    loadUserNames(id)
    load(true)
  },
)

onMounted(() => {
  if (!props.tenantId) return
  loadUserNames(props.tenantId)
  load(true)
})
</script>

<style scoped lang="less">
.operation-log {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-md);
}

.operation-log__toolbar {
  display: flex;
  align-items: center;
  gap: var(--app-space-sm);
}

.operation-log__filter {
  width: 200px;
}

.operation-log__search {
  width: 260px;
}

.operation-log__state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--app-space-sm);
  padding: var(--app-space-xl) 0;
}

.operation-log__target {
  display: flex;
  flex-direction: column;
}

.operation-log__muted {
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
}

.operation-log__more {
  display: flex;
  justify-content: center;
}
</style>
