import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listSystemAdmins,
  listUsers,
  resolveUserManagementError,
  type UserManagementListParams,
  type UserManagementListResponse,
  type UserManagementListScope,
  type WorkspaceRoleCounts,
} from '@/api/user-management'
import type { TenantRole } from '@/api/tenant/members'
import { useAuthStore } from '@/stores/auth'
import { useCapabilitiesStore } from '@/stores/capabilities'
import {
  EDITOR_BACKEND_ROLES,
  systemAdminToUserRow,
  toUserRow,
  type DirectoryEntry,
  type DirectoryRole,
  type UserRow,
} from './types'

const DEFAULT_PAGE_SIZE = 20
const SEARCH_DEBOUNCE_MS = 300
/** 超管列表接口单次最多返回 200 条；计数与过滤需要完整列表。 */
const SYSTEM_ADMIN_PAGE_SIZE = 200

const EMPTY_ROLE_COUNTS: WorkspaceRoleCounts = {
  viewer: 0,
  contributor: 0,
  admin: 0,
  owner: 0,
}

/**
 * 用户管理页的状态与数据加载。
 *
 * 权限只读 capabilities store 的投影（C4：前端只做界面门控）；
 * 真正的放行仍由后端逐接口校验。
 */
export function useUserManagement() {
  const { t } = useI18n()
  const auth = useAuthStore()
  const capabilities = useCapabilitiesStore()

  const selectedRole = ref<DirectoryRole>('owner')
  /** 空间筛选（Q34）：current 默认；other/all 仅系统管理员界面可见，后端放行。 */
  const scope = ref<UserManagementListScope>('current')
  const searchQuery = ref('')
  const page = ref(1)
  const pageSize = ref(DEFAULT_PAGE_SIZE)
  const rows = ref<UserRow[]>([])
  const total = ref(0)
  const roleCounts = ref<WorkspaceRoleCounts>({ ...EMPTY_ROLE_COUNTS })
  const systemAdminCount = ref(0)
  const loading = ref(false)
  const error = ref('')
  const initialized = ref(false)

  const tenantId = computed(() => auth.effectiveTenantId)
  const currentUserId = computed(() => auth.currentUserId)
  const canView = computed(() => capabilities.can('user.view'))
  const isSystemAdmin = computed(() => capabilities.isSystemAdmin)
  // 当前空间内的角色；系统管理员的能力由 isSystemAdmin 旁路，不依赖此值。
  const actorRole = computed<TenantRole | ''>(() => {
    const role = capabilities.role
    if (role === 'owner' || role === 'admin' || role === 'contributor' || role === 'viewer') {
      return role
    }
    return ''
  })
  const can = (capability: string) => capabilities.can(capability)
  const ownerCount = computed(() => roleCounts.value.owner)
  // 能力还没拉回来时先显示加载态，避免闪一下“无权限”。
  const capabilitiesPending = computed(() => !capabilities.loaded && capabilities.loading)

  // 只接受最后一次请求的结果，切换目录/搜索时旧请求直接丢弃。
  let requestSeq = 0
  let searchTimer: ReturnType<typeof setTimeout> | null = null

  // 目录计数；scope=other/all 时后端按视图内去重用户数返回，键可能缺 0 值。
  const directoryCount = (key: DirectoryRole): number => {
    const counts = roleCounts.value
    if (key === 'editor') return (counts.contributor ?? 0) + (counts.admin ?? 0)
    if (key === 'system_admin') return systemAdminCount.value
    return counts[key as 'owner' | 'viewer'] ?? 0
  }

  // 左目录（Q37）：owner 视角三类，超管视角另加系统管理员共四类；
  // 编辑 = contributor + admin 前端聚合。
  const directoryEntries = computed<DirectoryEntry[]>(() => {
    const entries: DirectoryEntry[] = []
    if (capabilities.isSystemAdmin) {
      entries.push({
        key: 'system_admin',
        label: t('userManagement.directory.systemAdmin'),
        count: systemAdminCount.value,
      })
    }
    entries.push(
      { key: 'owner', label: t('userManagement.directory.owner'), count: directoryCount('owner') },
      { key: 'editor', label: t('userManagement.directory.editor'), count: directoryCount('editor') },
      { key: 'viewer', label: t('userManagement.directory.viewer'), count: directoryCount('viewer') },
    )
    return entries
  })

  function clearSearchTimer() {
    if (searchTimer) {
      clearTimeout(searchTimer)
      searchTimer = null
    }
  }

  /** 分页拉完整个超管列表：接口不支持搜索/角色筛选，搜索在本地做。 */
  async function fetchAllSystemAdmins(): Promise<UserRow[]> {
    const collected: UserRow[] = []
    let offset = 0
    let reportedTotal = 0
    do {
      const response = await listSystemAdmins({ offset, limit: SYSTEM_ADMIN_PAGE_SIZE })
      reportedTotal = response.total ?? 0
      const admins = response.admins ?? []
      collected.push(...admins.map(systemAdminToUserRow))
      offset += admins.length
      if (admins.length === 0) break
    } while (collected.length < reportedTotal)
    systemAdminCount.value = reportedTotal
    return collected
  }

  function filterSystemAdmins(items: UserRow[]): UserRow[] {
    const keyword = searchQuery.value.trim().toLowerCase()
    if (!keyword) return items
    return items.filter((row) => {
      const email = row.email?.toLowerCase() ?? ''
      const username = row.username?.toLowerCase() ?? ''
      return email.includes(keyword) || username.includes(keyword)
    })
  }

  function parseListResponse(response: UserManagementListResponse) {
    if (!response.success || !response.data) {
      throw new Error(response.message || 'user management list failed')
    }
    return response.data
  }

  /**
   * 目录对应的列表请求。后端 role 参数不支持多值，「编辑」目录按
   * contributor/admin 各拉一页再前端合并：total 取两份之和，
   * 每页最多出现两页条目，分页语义保持一致（Q37）。
   */
  async function fetchDirectoryList(
    tenantId: number | string,
  ): Promise<{ items: UserRow[]; total: number; roleCounts?: WorkspaceRoleCounts }> {
    const base: UserManagementListParams = {
      scope: scope.value,
      q: searchQuery.value.trim() || undefined,
      page: page.value,
      page_size: pageSize.value,
    }
    if (selectedRole.value !== 'editor') {
      const data = parseListResponse(await listUsers(tenantId, {
        ...base,
        role: selectedRole.value as TenantRole,
      }))
      return {
        items: (data.items ?? []).map(toUserRow),
        total: data.total ?? 0,
        roleCounts: data.role_counts,
      }
    }
    const responses = await Promise.all(
      EDITOR_BACKEND_ROLES.map((role) => listUsers(tenantId, { ...base, role })),
    )
    const dataList = responses.map(parseListResponse)
    return {
      items: dataList.flatMap((data) => (data.items ?? []).map(toUserRow)),
      total: dataList.reduce((sum, data) => sum + (data.total ?? 0), 0),
      roleCounts: dataList[0]?.role_counts,
    }
  }

  async function load() {
    if (!canView.value) return
    const currentSeq = ++requestSeq
    loading.value = true
    error.value = ''
    try {
      if (selectedRole.value === 'system_admin') {
        const all = await fetchAllSystemAdmins()
        if (currentSeq !== requestSeq) return
        const filtered = filterSystemAdmins(all)
        total.value = filtered.length
        const start = (page.value - 1) * pageSize.value
        rows.value = filtered.slice(start, start + pageSize.value)
      } else {
        const currentTenantId = tenantId.value
        if (currentTenantId == null) {
          rows.value = []
          total.value = 0
          return
        }
        const result = await fetchDirectoryList(currentTenantId)
        if (currentSeq !== requestSeq) return
        rows.value = result.items
        total.value = result.total
        if (result.roleCounts) {
          roleCounts.value = result.roleCounts
        }
      }
      initialized.value = true
    } catch (loadError) {
      if (currentSeq !== requestSeq) return
      error.value = resolveUserManagementError(loadError)
      rows.value = []
      total.value = 0
    } finally {
      if (currentSeq === requestSeq) loading.value = false
    }
  }

  /** 超管目录的计数在未选中该目录时也要显示，单独取一次 total。 */
  async function loadSystemAdminCount() {
    if (!capabilities.isSystemAdmin) return
    try {
      const response = await listSystemAdmins({ offset: 0, limit: 1 })
      systemAdminCount.value = response.total ?? 0
    } catch {
      // 计数只是目录提示，失败不影响当前表格；选中超管目录时
      // 真正的加载错误会由 load() 展示。
    }
  }

  function selectRole(role: DirectoryRole) {
    if (role === selectedRole.value) return
    if (role === 'system_admin' && !capabilities.isSystemAdmin) return
    clearSearchTimer()
    selectedRole.value = role
    page.value = 1
    void load()
  }

  /** 切换空间筛选（Q34）：回到第一页重新加载；other/all 仅超管可见，入口由界面门控。 */
  function selectScope(next: UserManagementListScope) {
    if (next === scope.value) return
    if (next !== 'current' && !capabilities.isSystemAdmin) return
    clearSearchTimer()
    scope.value = next
    page.value = 1
    void load()
  }

  /**
   * 搜索防抖后回到第一页再查。翻页不改搜索词，所以不会触发。
   */
  watch(searchQuery, () => {
    clearSearchTimer()
    if (!canView.value) return
    searchTimer = setTimeout(() => {
      searchTimer = null
      page.value = 1
      void load()
    }, SEARCH_DEBOUNCE_MS)
  })

  async function onPageChange() {
    clearSearchTimer()
    await load()
  }

  /** 交接弹窗的候选人：本空间全部 active 成员（最多拉 20 页兜底）。 */
  async function loadTransferCandidates(): Promise<UserRow[]> {
    const currentTenantId = tenantId.value
    if (currentTenantId == null) return []
    const collected: UserRow[] = []
    for (let pageNo = 1; pageNo <= 20; pageNo++) {
      const response = await listUsers(currentTenantId, { page: pageNo, page_size: 100 })
      if (!response.success || !response.data) break
      const items = (response.data.items ?? []).map(toUserRow)
      collected.push(...items)
      const total = response.data.total ?? 0
      if (items.length < 100 || collected.length >= total) break
    }
    return collected.filter((row) => row.status === 'active')
  }

  /** 重新拉能力与当前列表；操作成功后保持页码/搜索/目录不变。 */
  async function reload() {
    await Promise.all([loadSystemAdminCount(), load()])
  }

  async function init() {
    await capabilities.refresh()
    if (!canView.value) return
    await Promise.all([loadSystemAdminCount(), load()])
  }

  onMounted(() => {
    void init()
  })

  // 切换空间后能力与列表都可能变化，重新取一次。
  watch(tenantId, async (current, previous) => {
    if (current === previous) return
    await capabilities.refresh()
    if (!canView.value) {
      rows.value = []
      total.value = 0
      return
    }
    page.value = 1
    await Promise.all([loadSystemAdminCount(), load()])
  })

  onBeforeUnmount(() => {
    clearSearchTimer()
    // 让还没回来的请求失效，避免卸载后再写状态。
    requestSeq++
  })

  return {
    can,
    canView,
    tenantId,
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
    initialized,
    load,
    reload,
    loadTransferCandidates,
    selectRole,
    onPageChange,
  }
}
