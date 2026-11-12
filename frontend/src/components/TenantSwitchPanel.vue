<template>
  <Teleport to="body">
    <!-- 空间切换浮动面板：仅超管渲染（superAdmin=false 时整个面板不渲染）。
         根节点保留 tenant-submenu-floating 类名，供 UserMenu 的点击外部关闭
         判定继续命中；tenant-switch-panel 供测试与新样式使用。 -->
    <div v-if="open && superAdmin" ref="panelRef" class="tenant-submenu-floating tenant-switch-panel"
      :style="panelStyle" @mouseenter="emit('pointer-enter')" @mouseleave="emit('pointer-leave')">
      <div class="tenant-submenu-header">
        <div class="tenant-switch-title">{{ $t('tenant.switcher.menuLabel') }}</div>
        <!-- 当前空间始终可见（AC-34）：列表处于加载/空/错误态时用户仍知道自己在哪。 -->
        <div class="tenant-switch-current">{{ $t('tenant.currentTenant') }}: {{ currentTenantName }}</div>
      </div>
      <div class="tenant-switch-search">
        <t-icon name="search" class="tenant-switch-search-icon" />
        <input ref="searchInputRef" v-model="searchQuery" type="text" class="tenant-switch-search-input"
          :placeholder="$t('tenant.searchPlaceholder')" :aria-label="$t('tenant.searchPlaceholder')"
          @input="handleSearchInput" @keydown.esc="emit('close')" />
        <t-icon v-if="searchQuery" name="close-circle-filled" class="tenant-switch-clear" role="button" tabindex="0"
          :aria-label="$t('tenant.searchPlaceholder')" @click="clearSearch" @keydown.enter.prevent="clearSearch" />
      </div>
      <div ref="listRef" class="tenant-submenu-list" @scroll="handleScroll">
        <!-- 列表四态（AC-34）：加载 / 错误 / 搜索无结果 / 空，收敛为一个状态块。 -->
        <div v-if="listStatus !== ''" class="tenant-switch-status" :class="{ 'is-error': statusIsError }">
          <t-loading v-if="statusIsLoading" size="small" />
          <span>{{ listStatus }}</span>
        </div>
        <div v-for="tenant in tenants" :key="tenant.id" class="tenant-submenu-item"
          :class="{ 'is-current': isCurrent(tenant.id) }" role="button" tabindex="0"
          :aria-current="isCurrent(tenant.id) ? 'true' : undefined"
          @click="selectTenant(tenant)" @keydown.enter.prevent="selectTenant(tenant)"
          @keydown.space.prevent="selectTenant(tenant)">
          <div class="tenant-submenu-item-avatar" :class="{ 'is-current': isCurrent(tenant.id) }">
            {{ tenantInitial(tenant) }}
            <!-- Home 标识：home 空间行的 avatar 右下角小 icon，指明主空间在哪行。 -->
            <span v-if="isHomeTenant(tenant.id)" class="tenant-submenu-item-home-dot"
              :title="$t('tenant.switcher.homeTooltip')">
              <t-icon name="home" size="9px" />
            </span>
          </div>
          <!-- 两行布局：空间名拿满宽度；次行空间 ID + 「当前」徽标，不显示角色（AC-29）。 -->
          <div class="tenant-submenu-item-info">
            <span class="tenant-submenu-item-name">{{ tenantDisplayName(tenant) }}</span>
            <div class="tenant-submenu-item-meta">
              <span class="tenant-submenu-item-id">#{{ tenant.id }}</span>
              <span v-if="isCurrent(tenant.id)" class="tenant-submenu-item-badge">{{
                $t('tenant.switcher.currentBadge') }}</span>
            </div>
          </div>
        </div>
        <div v-if="loading && tenants.length > 0" class="tenant-switch-loading-more">
          <t-loading size="small" />
        </div>
      </div>
      <!-- 自助创建入口与 /auth/me 返回的后端能力保持一致。 -->
      <div v-if="authStore.canCreateTenant" class="tenant-submenu-create" role="button" tabindex="0"
        @click="openCreateDialog" @keydown.enter.prevent="openCreateDialog" @keydown.space.prevent="openCreateDialog">
        <t-icon name="add" class="tenant-submenu-create-icon" />
        <span class="tenant-submenu-create-label">{{ $t('tenant.create.action') }}</span>
      </div>
    </div>
    <CreateTenantDialog v-model:visible="createDialogVisible" @created="onTenantCreated" />
  </Teleport>
</template>

<script setup lang="ts">
// 共享空间切换面板（T10）：由 UserMenu 的超管浮动面板与 TenantSelector 的
// 搜索/分页合并而成。超管数据源 searchTenants（全部可访问空间，支持关键词
// 与纯数字空间 ID 搜索 + 滚动分页）；非超管不渲染列表（open 由父级控制，
// superAdmin=false 时整个面板不出现在 DOM）。切换链路与 utils/tenantSwitch.ts
// 保持一致：setSelectedTenant + last_active 持久化 + 整页刷新。
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import { searchTenants, type TenantInfo } from '@/api/tenant'
import CreateTenantDialog from '@/components/CreateTenantDialog.vue'
import { useRoleLabel, useHomeTenant } from '@/composables/useRoleLabel'
import {
  navigateAfterTenantSwitch,
  persistLastActiveTenantPreference,
  stashTenantSwitchToast,
} from '@/utils/tenantSwitch'
import { getRootZoom, rectToCssPx, cssViewportSize } from '@/utils/zoom'

// props：open 受控；anchor 是定位锚点；superAdmin 决定是否渲染面板与数据源；
// currentTenantId 用于「当前」高亮与徽标。
const props = withDefaults(defineProps<{
  open: boolean
  anchor?: HTMLElement | null
  superAdmin?: boolean
  currentTenantId?: number | null
}>(), { anchor: null, superAdmin: false, currentTenantId: null })

// close：面板请求关闭（Esc / 点当前行 / 切换或创建前）；
// select / created：点击空间行 / 新建空间成功（切换链路执行前发出）；
// pointer-enter / pointer-leave：面板 hover 进出，父级维持 hover 开合延时。
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select', payload: { tenantId: number; name: string }): void
  (e: 'created', payload: { tenantId: number; name: string }): void
  (e: 'pointer-enter'): void
  (e: 'pointer-leave'): void
}>()

const { t } = useI18n()
const authStore = useAuthStore()
const { formatRole } = useRoleLabel()
const { homeTenantId, isHomeTenant } = useHomeTenant()

// ---------- 列表数据：searchTenants 搜索 / 分页 / 滚动加载 ----------
const searchQuery = ref('')
const tenants = ref<TenantInfo[]>([])
const currentPage = ref(1)
const total = ref(0)
const loading = ref(false)
const loadError = ref('')
let searchTimer: number | null = null
const PAGE_SIZE = 20

const listRef = ref<HTMLElement | null>(null)
const searchInputRef = ref<HTMLInputElement | null>(null)

const hasMore = computed(() => tenants.value.length < total.value)
const statusIsLoading = computed(() => loading.value && tenants.value.length === 0)
const statusIsError = computed(() =>
  !statusIsLoading.value && loadError.value !== '' && tenants.value.length === 0)

// 状态块文案：加载 / 错误 / 搜索无结果 / 空；有数据时返回空串不渲染。
const listStatus = computed(() => {
  if (statusIsLoading.value) return t('tenant.loading')
  if (statusIsError.value) return loadError.value
  if (tenants.value.length === 0) {
    return searchQuery.value.trim() !== '' ? t('tenant.noMatch') : t('tenant.switcher.empty')
  }
  return ''
})

const loadTenants = async (append = false) => {
  if (loading.value) return
  loading.value = true
  if (!append) loadError.value = ''
  try {
    const keyword = searchQuery.value.trim()
    // 纯数字关键词同时作为空间 ID 精确过滤（沿用 TenantSelector 口径）。
    const tenantID = keyword && /^\d+$/.test(keyword) ? Number(keyword) : undefined
    const response = await searchTenants({
      keyword: keyword || undefined,
      tenant_id: tenantID,
      page: currentPage.value,
      page_size: PAGE_SIZE,
    })
    if (response.success && response.data) {
      tenants.value = append ? [...tenants.value, ...response.data.items] : response.data.items
      total.value = response.data.total
      authStore.setAllTenants(tenants.value)
    } else {
      handleLoadFailure(append, response.message)
    }
  } catch (error) {
    console.error('Failed to load tenants:', error)
    handleLoadFailure(append)
  } finally {
    loading.value = false
  }
}

// 失败提示（AC-34）：首屏失败渲染内联错误态；加载更多失败用 toast 提示。
const handleLoadFailure = (append: boolean, message?: string) => {
  const fallback = message || t('tenant.loadTenantsFailed')
  if (append) MessagePlugin.error(fallback)
  else loadError.value = fallback
}

// 搜索输入 300ms 防抖后重置分页重查。
const handleSearchInput = () => {
  if (searchTimer !== null) clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    searchTimer = null
    currentPage.value = 1
    tenants.value = []
    total.value = 0
    void loadTenants()
  }, 300)
}

const clearSearch = () => {
  searchQuery.value = ''
  currentPage.value = 1
  tenants.value = []
  total.value = 0
  void loadTenants()
}

// 滚动接近底部时加载下一页。
const handleScroll = () => {
  const el = listRef.value
  if (!el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
  if (nearBottom && hasMore.value && !loading.value) {
    currentPage.value++
    void loadTenants(true)
  }
}

// ---------- 当前空间与显示辅助 ----------
const currentTenantName = computed(() => authStore.selectedTenantName ||
  authStore.tenant?.name ||
  (props.currentTenantId != null ? `#${props.currentTenantId}` : ''))

const isCurrent = (id: number) =>
  props.currentTenantId != null && Number(props.currentTenantId) === Number(id)

const tenantDisplayName = (tenant: TenantInfo) => (tenant.name || '').trim() || `#${tenant.id}`

const tenantInitial = (tenant: TenantInfo) =>
  (tenantDisplayName(tenant).charAt(0) || '?').toUpperCase()

// ---------- 切换链路（switchToTenant / selectTenant 合并实现） ----------
const selectTenant = (tenant: TenantInfo) => {
  const displayName = tenantDisplayName(tenant)
  if (isCurrent(tenant.id)) {
    emit('close')
    return
  }
  emit('select', { tenantId: tenant.id, name: displayName })
  // 超管在目标空间可能没有 tenant_members 记录；有则取来给 toast 用角色。
  const membership = (authStore.memberships ?? []).find(
    (m) => Number(m.tenant_id) === tenant.id)
  // 始终写 override 让 request.ts 附 X-Tenant-ID，不因切回 home 就清空（否则
  // JWT 在 last_active != home 的会话里会落在 peer 空间，切回 home 原地不动）。
  // 服务端持久化偏好仍区分：切回 home 清 last_active，下次干净重登回 home。
  const switchingToHome = homeTenantId.value !== null && homeTenantId.value === tenant.id
  const homeNameFallback = switchingToHome ? membership?.tenant_name ?? null : null
  authStore.setSelectedTenant(tenant.id, (tenant.name || '').trim() || homeNameFallback)
  emit('close')
  // Toast 跨整页刷新传递：reload 前落 sessionStorage，App.vue 启动时弹出。
  stashTenantSwitchToast({
    name: displayName,
    role: membership ? formatRole(membership.role) : undefined,
    roleEnum: membership?.role || undefined,
  })
  // 持久化与 400ms 宽限竞速，尽量在页面卸载前写完；随后整页刷新重置所有
  // store / SSE / 在途请求并跳到平台首页。
  const persist = persistLastActiveTenantPreference(switchingToHome ? null : tenant.id)
  Promise.race([persist, new Promise((resolve) => setTimeout(resolve, 400))])
    .finally(() => navigateAfterTenantSwitch())
}

// ---------- 创建空间 ----------
const createDialogVisible = ref(false)

const openCreateDialog = () => {
  if (!authStore.canCreateTenant) {
    MessagePlugin.info(t('tenant.create.disabled'))
    return
  }
  createDialogVisible.value = true
}

// 新建后走与 selectTenant 同款链路：刷新 /auth/me 再切到新空间。
const onTenantCreated = async (newTenant: TenantInfo) => {
  emit('created', { tenantId: newTenant.id, name: newTenant.name })
  await authStore.refreshFromAuthMe()
  authStore.setSelectedTenant(newTenant.id, newTenant.name)
  const persist = persistLastActiveTenantPreference(newTenant.id)
  Promise.race([persist, new Promise((resolve) => setTimeout(resolve, 300))])
    .finally(() => navigateAfterTenantSwitch())
}

// ---------- 浮动定位 + 视口夹取 ----------
const panelRef = ref<HTMLElement | null>(null)
const panelStyle = ref<Record<string, string>>({})

// 面板 position:fixed 挂在根缩放下；锚点矩形是视觉像素，先归一化成 CSS 像素
// 再写回样式。水平方向优先放锚点右侧，放不下翻到左侧。
const positionPanel = () => {
  const el = props.anchor
  if (!el) return
  const zoom = getRootZoom()
  const rect = rectToCssPx(el.getBoundingClientRect(), zoom)
  const { width: vw } = cssViewportSize(zoom)
  const PANEL_WIDTH = 264
  const GAP = 8
  const MARGIN = 8
  let left = rect.right + GAP
  if (left + PANEL_WIDTH + MARGIN > vw) {
    left = Math.max(MARGIN, rect.left - PANEL_WIDTH - GAP)
  }
  panelStyle.value = { left: `${left}px`, top: `${Math.max(MARGIN, rect.top)}px` }
}

// 垂直方向夹取：面板渲染后按实际高度限制 top，保证贴近视口底时不溢出。
const clampToViewport = () => {
  requestAnimationFrame(() => {
    const panel = panelRef.value
    if (!panel) return
    const MARGIN = 8
    const { height: vh } = cssViewportSize()
    const maxTop = vh - panel.offsetHeight - MARGIN
    const currentTop = parseFloat(panelStyle.value.top || '0') || 0
    if (currentTop > maxTop) {
      panelStyle.value = { ...panelStyle.value, top: `${Math.max(MARGIN, maxTop)}px` }
    }
  })
}

// ---------- 打开 / 关闭联动 ----------
let lastMembershipRefresh = 0
const MEMBERSHIP_REFRESH_MS = 2000

// 打开时限流刷新 /auth/me（2 秒内重复开合不重复请求）。
const refreshMembershipsThrottled = () => {
  const now = Date.now()
  if (now - lastMembershipRefresh >= MEMBERSHIP_REFRESH_MS) {
    lastMembershipRefresh = now
    void authStore.refreshFromAuthMe()
  }
}

const resetSearchState = () => {
  searchQuery.value = ''
  currentPage.value = 1
  loadError.value = ''
  if (searchTimer !== null) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
}

watch(() => props.open, (open) => {
  if (!open) {
    resetSearchState()
    return
  }
  positionPanel()
  void nextTick(() => searchInputRef.value?.focus())
  clampToViewport()
  if (tenants.value.length === 0) void loadTenants()
  refreshMembershipsThrottled()
}, { immediate: true })

onUnmounted(() => {
  if (searchTimer !== null) clearTimeout(searchTimer)
})
</script>

<style lang="less">
// 空间切换面板样式随组件走（teleport 到 body，全局作用域）。
.tenant-submenu-floating {
  position: fixed;
  z-index: 1100;
  width: 264px;
  max-height: 340px;
  display: flex;
  flex-direction: column;
  background: var(--td-bg-color-container);
  border: 0.5px solid var(--td-component-stroke);
  border-radius: var(--app-radius-lg);
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.12);
  // 指针桥：滑过锚点与面板之间的缝隙不触发 mouseleave 关闭。
  padding-left: 2px;
  overflow: hidden;

  .tenant-submenu-header { padding: 8px 12px 6px; border-bottom: 0.5px solid var(--td-component-stroke); }
  .tenant-switch-title { font-size: var(--app-text-sm); font-weight: 600; color: var(--td-text-color-secondary); }
  // 当前空间行：列表任意状态下都可见（AC-34）。
  .tenant-switch-current {
    margin-top: 2px; font-size: var(--app-text-2xs); color: var(--td-text-color-placeholder);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }

  .tenant-switch-search {
    display: flex; align-items: center; gap: 6px;
    margin: 6px 8px 2px; padding: 6px 10px;
    background: var(--td-bg-color-secondarycontainer);
    border: 0.5px solid transparent; border-radius: var(--app-radius-sm);
    transition: all var(--app-motion-base);

    &:focus-within {
      background: var(--td-bg-color-container);
      border-color: var(--td-brand-color);
      box-shadow: 0 0 0 2px color-mix(in srgb, var(--td-brand-color) 10%, transparent);
    }
  }

  .tenant-switch-search-icon { font-size: var(--app-text-base); color: var(--td-text-color-placeholder); flex-shrink: 0; }

  .tenant-switch-search-input {
    flex: 1; min-width: 0;
    border: none; outline: none; background: transparent;
    font-size: var(--app-text-md); color: var(--td-text-color-primary);
    &::placeholder { color: var(--td-text-color-placeholder); }
  }
  .tenant-switch-clear {
    font-size: var(--app-text-base); color: var(--td-text-color-placeholder);
    cursor: pointer; flex-shrink: 0; transition: color var(--app-motion-base);
    &:hover { color: var(--td-text-color-secondary); }
  }

  .tenant-submenu-list { flex: 1; overflow-y: auto; padding: 4px; }

  .tenant-submenu-item {
    display: flex; align-items: center; gap: 8px;
    padding: 7px 8px;
    border-radius: var(--app-radius-sm);
    cursor: pointer;
    transition: background var(--app-motion-fast);

    &:hover, &:focus-visible { background: var(--td-bg-color-secondarycontainer); outline: none; }

    &.is-current {
      background: var(--td-bg-color-secondarycontainer);
      cursor: default;
      .tenant-submenu-item-name { color: var(--td-text-color-primary); font-weight: 600; }
    }
  }

  .tenant-submenu-item-avatar {
    position: relative;
    width: 28px; height: 28px;
    border-radius: var(--app-radius-sm);
    background: var(--td-bg-color-secondarycontainer);
    display: flex; align-items: center; justify-content: center;
    font-size: var(--app-text-md); font-weight: 600;
    color: var(--td-text-color-secondary);
    flex-shrink: 0;

    &.is-current {
      background: linear-gradient(135deg, var(--td-brand-color) 0%, var(--td-brand-color-active) 100%);
      color: var(--td-text-color-anti);
    }
  }

  .tenant-submenu-item-info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }

  .tenant-submenu-item-name {
    font-size: var(--app-text-md);
    color: var(--td-text-color-primary);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }

  // 次行：空间 ID + 「当前」徽标，以 inline 形式排在一起。
  .tenant-submenu-item-meta { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .tenant-submenu-item-id { font-size: var(--app-text-xs); color: var(--td-text-color-placeholder); }

  .tenant-submenu-item-badge {
    flex-shrink: 0;
    font-size: var(--app-text-2xs); font-weight: 600; line-height: 1.2; padding: 2px 6px;
    border-radius: var(--app-radius-xs);
    background: var(--td-bg-color-component); color: var(--td-text-color-secondary);
  }

  // Home 标识：叠在 avatar 右下角的小 dot，不占次行宽度。
  .tenant-submenu-item-home-dot {
    position: absolute; right: -3px; bottom: -3px;
    width: 14px; height: 14px; border-radius: 50%;
    background: var(--td-bg-color-container); color: var(--td-text-color-secondary);
    border: 1.5px solid var(--td-bg-color-container);
    display: flex; align-items: center; justify-content: center;
    pointer-events: none;
    box-shadow: 0 0 0 0.5px var(--td-success-color-light);
  }

  // 列表状态块：加载 / 错误 / 搜索无结果 / 空。
  .tenant-switch-status {
    display: flex; align-items: center; justify-content: center; gap: 8px;
    padding: 18px 10px;
    font-size: var(--app-text-sm);
    color: var(--td-text-color-placeholder);
    &.is-error { color: var(--td-error-color); }
  }

  .tenant-switch-loading-more { display: flex; justify-content: center; padding: 8px; }

  .tenant-submenu-create {
    display: flex; align-items: center; gap: 6px;
    padding: 8px 10px; margin: 3px 4px 5px;
    border-top: .5px solid var(--td-component-stroke);
    border-radius: var(--app-radius-sm);
    cursor: pointer; color: var(--td-brand-color);
    font-size: var(--app-text-base); font-weight: 500;
    transition: background var(--app-motion-fast);

    &:hover, &:focus-visible {
      background: color-mix(in srgb, var(--td-brand-color) 8%, transparent);
      outline: none;
    }

    .tenant-submenu-create-icon { font-size: var(--app-text-xl); flex-shrink: 0; }

    .tenant-submenu-create-label {
      flex: 1;
      white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
      font-size: var(--app-text-sm);
    }
  }
}
</style>
