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

    <div v-else-if="visibleRows.length === 0 && !hasMore" class="operation-log__state">
      <t-empty :description="t('userManagement.operations.empty')" />
    </div>

    <template v-else>
      <t-table v-if="visibleRows.length > 0" row-key="id" :data="visibleRows" :columns="columns" size="medium" hover>
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
import { computed, onActivated, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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

// 关键词由后端按 action / 操作人 / 目标匹配，前端只保留成员操作类型的过滤。
const visibleRows = computed(() =>
  entries.value.filter((row) => (MEMBER_ACTIONS as readonly string[]).includes(row.action)),
)

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
    if (disposed) return
    const map: Record<string, string> = {}
    for (const item of resp.data?.items ?? []) {
      map[item.user_id] = item.email || item.username || item.user_id
    }
    userNames.value = map
  } catch {
    // 回填失败不影响记录本身展示。
  }
}

// 关键词/筛选/空间切换都会并发触发请求：用请求序号保证只有最新一轮的
// 响应能写回。旧响应（包括错误）一律丢弃，避免新关键词被旧结果覆盖。
let requestSeq = 0
// 组件卸载后迟到的请求不得再写状态（KeepAlive 切走不等同卸载，这里只在
// 真正 unmount 时置位）。
let disposed = false

function beginRequest(): number {
  requestSeq += 1
  return requestSeq
}

function isLatestRequest(seq: number): boolean {
  return seq === requestSeq
}

// 让所有飞行中的请求立即失效：它们随后的响应/错误/finally 都会被
// isLatestRequest 丢弃，不再写回状态。
function invalidateInFlight(): void {
  requestSeq += 1
}

async function load(reset: boolean) {
  const tenantId = props.tenantId
  if (!tenantId) return
  // 关键词防抖等待期间禁止「加载更多」：此时的游标属于旧关键词，拼上新
  // 关键词会串数据；等防抖到点后由 load(true) 从第一页重新请求。
  if (!reset && keywordTimer !== null) return
  // 「加载更多」重复点击仍然忽略；重置类请求（关键词/筛选/刷新）即使上一轮
  // 还在 loading 也必须发起新一轮，否则新关键词会被旧请求挡掉。
  if (!reset && loading.value) return
  const seq = beginRequest()
  loading.value = true
  error.value = ''
  try {
    const resp = await listAuditLog(tenantId, {
      after_id: reset ? undefined : cursor.value || undefined,
      limit: PAGE_SIZE,
      action: actionFilter.value || undefined,
      q: keyword.value.trim() || undefined,
    })
    if (!isLatestRequest(seq)) return
    if (!resp.success) throw new Error(resp.message || '')
    const rows = resp.data ?? []
    entries.value = reset ? rows : [...entries.value, ...rows]
    cursor.value = resp.next_cursor ?? 0
    hasMore.value = Boolean(resp.next_cursor)
  } catch (err) {
    if (!isLatestRequest(seq)) return
    error.value = resolveUserManagementError(err)
  } finally {
    if (isLatestRequest(seq)) loading.value = false
  }
}

function clearKeywordTimer() {
  if (keywordTimer) {
    clearTimeout(keywordTimer)
    keywordTimer = null
  }
}

// 筛选变化立即重置游标；同时取消尚未触发的关键词防抖，避免连发两次。
watch(actionFilter, () => {
  clearKeywordTimer()
  void load(true)
})

// 关键词交给后端搜索：300ms 防抖并回到第一页。关键词一变就立即作废飞行中
// 的旧请求（不能等防抖到点），否则旧响应会在防抖窗口内抢先写回旧结果。
let keywordTimer: ReturnType<typeof setTimeout> | null = null
watch(keyword, () => {
  clearKeywordTimer()
  invalidateInFlight()
  keywordTimer = setTimeout(() => {
    keywordTimer = null
    void load(true)
  }, 300)
})
watch(
  () => props.tenantId,
  (id) => {
    if (!id) return
    void loadUserNames(id)
    void load(true)
  },
)

onMounted(() => {
  if (!props.tenantId) return
  void loadUserNames(props.tenantId)
  void load(true)
})

// KeepAlive 切走再切回时组件不重建，onMounted 不会重跑；首次激活与
// onMounted 重叠，用标记跳过，避免首屏双请求。
let activatedOnce = false
onActivated(() => {
  if (!activatedOnce) {
    activatedOnce = true
    return
  }
  if (!props.tenantId) return
  void load(true)
})

onBeforeUnmount(() => {
  clearKeywordTimer()
  // 让飞行中的请求失效，避免卸载后写回状态。
  disposed = true
  invalidateInFlight()
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
