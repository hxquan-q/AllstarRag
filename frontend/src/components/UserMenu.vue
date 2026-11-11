<template>
  <div class="user-menu" :class="{ 'user-menu--collapsed': uiStore.sidebarCollapsed }" ref="menuRef">
    <div class="user-bar">
    <!-- 用户按钮 -->
    <div class="user-button" data-guide="user-menu" @click="toggleMenu">
      <div class="user-avatar">
        <img v-if="userAvatar" :src="userAvatar" :alt="$t('common.avatar')" />
        <span v-else class="avatar-placeholder">{{ userInitial }}</span>
      </div>
      <div v-if="!uiStore.sidebarCollapsed" class="user-info">
          <!-- 多空间 / superuser：首行空间名，次行 username · 角色。单空间：昵称 + 邮箱。 -->
          <template v-if="showTenantIdentityLine">
            <div class="user-tenant-name" :title="activeTenantName">{{ activeTenantName }}</div>
            <div class="user-tenant-meta">
              <span v-if="userName && userName !== activeTenantName" class="user-tenant-meta-name">{{ userName }}</span>
              <span v-if="(userName && userName !== activeTenantName) && currentRoleLabel"
                class="user-tenant-meta-sep">·</span>
              <t-icon v-if="currentRoleIcon" :name="currentRoleIcon" size="12px" class="user-tenant-meta-icon" />
              <span v-if="currentRoleLabel" class="user-tenant-meta-role">{{ currentRoleLabel }}</span>
            </div>
          </template>
          <template v-else>
            <div class="user-name">{{ userName }}</div>
            <div class="user-email">{{ userEmail }}</div>
          </template>
        </div>
    </div>
    <GlobalInvitationBell v-if="!uiStore.sidebarCollapsed" v-slot="{ count, open }">
      <t-tooltip :content="t('notifications.title')" placement="top">
        <button type="button" class="user-notify" :class="{ 'is-open': open }"
          :aria-label="t('notifications.title')" @click.stop>
          <t-badge :count="count" :max-count="99" size="small" :offset="[2, -2]">
            <t-icon name="notification" size="16px" />
          </t-badge>
        </button>
      </t-tooltip>
    </GlobalInvitationBell>
    </div>
    <GlobalInvitationBell v-if="uiStore.sidebarCollapsed" v-slot="{ count, open }">
      <t-tooltip :content="t('notifications.title')" placement="right">
        <button type="button" class="user-notify" :class="{ 'is-open': open }"
          :aria-label="t('notifications.title')" @click.stop>
          <t-icon name="notification" size="16px" />
          <i v-if="count > 0" class="user-notify__dot" aria-hidden="true" />
        </button>
      </t-tooltip>
    </GlobalInvitationBell>

    <!-- 下拉菜单 -->
    <Transition name="dropdown">
      <div v-if="menuVisible" class="user-dropdown" @click.stop>
        <!-- 弹出菜单：账号（头像+昵称）／当前空间（名称+权限）；底部侧栏样式不改。 -->
        <div v-if="userName" class="dropdown-user-header is-clickable" role="button" tabindex="0"
          @click="handleQuickNav('userprofile')" @keydown.enter.prevent="handleQuickNav('userprofile')"
          @keydown.space.prevent="handleQuickNav('userprofile')">
          <div class="dropdown-user-avatar">
            <img v-if="userAvatar" :src="userAvatar" :alt="$t('common.avatar')" />
            <span v-else class="dropdown-user-avatar-placeholder">{{ userInitial }}</span>
          </div>
          <div class="dropdown-user-meta">
            <div class="dropdown-user-name-row">
              <span class="dropdown-user-name">{{ userName }}</span>
              <!-- 客户版没有新手引导，入口一并隐藏；管理员保持原样。 -->
              <t-tooltip v-if="!isCustomer" :content="$t('newUserGuide.reopen')" placement="top">
                <button type="button" class="dropdown-guide-btn" :aria-label="$t('newUserGuide.reopen')"
                  @click.stop="reopenGuide">
                  <t-icon name="help-circle" size="14px" />
                </button>
              </t-tooltip>
            </div>
            <span v-if="userEmail" class="dropdown-user-email">{{ userEmail }}</span>
          </div>
        </div>

        <div v-if="userName && !authStore.isLiteMode && !isCustomer" ref="tenantMenuItemRef" class="dropdown-tenant-panel" :class="{
          'is-open': tenantSubmenuOpen,
          'is-clickable': showTenantSwitcher,
        }" :role="showTenantSwitcher ? 'button' : undefined" :tabindex="showTenantSwitcher ? 0 : undefined"
          @click="showTenantSwitcher && showTenantSubmenu()"
          @keydown.enter.prevent="showTenantSwitcher && showTenantSubmenu()"
          @keydown.space.prevent="showTenantSwitcher && showTenantSubmenu()"
          @mouseenter="showTenantSwitcher && showTenantSubmenu()"
          @mouseleave="showTenantSwitcher && scheduleHideTenantSubmenu()">
          <t-icon name="system-sum" class="menu-icon" aria-hidden="true" />
          <div class="dropdown-tenant-panel-main">
            <span class="dropdown-tenant-panel-name" :title="activeTenantName || userName">
              {{ activeTenantName || userName }}
            </span>
            <div v-if="currentRoleLabel" class="dropdown-tenant-panel-role">
              <t-icon v-if="currentRoleIcon" :name="currentRoleIcon" size="12px"
                class="dropdown-tenant-panel-role-icon" />
              <span>{{ currentRoleLabel }}</span>
            </div>
          </div>
          <t-icon v-if="showTenantSwitcher" name="swap" class="dropdown-tenant-panel-trail"
            :title="$t('tenant.switcher.menuLabel')" />
        </div>
        <div class="menu-divider"></div>
        <!-- 账号与空间是头像菜单的核心上下文；基础设施类配置统一收进「全部设置」。 -->
        <div class="menu-item" @click="handleQuickNav('general')">
          <t-icon name="user" class="menu-icon" />
          <span>{{ $t('general.personalSettings') }}</span>
        </div>
        <!-- 客户直达「产线」设置分区（AC-11）；管理员端不出现该入口（C6）。 -->
        <div v-if="isCustomer" class="menu-item" @click="handleQuickNav('agentlines')">
          <t-icon name="control-platform" class="menu-icon" />
          <span>{{ $t('agentLineSettings.menuLabel') }}</span>
        </div>
        <div v-if="!authStore.isLiteMode && !isCustomer" class="menu-item" @click="handleQuickNav('tenant')">
          <t-icon name="user-circle" class="menu-icon" />
          <span>{{ $t('settings.workspaceSettings') }}</span>
        </div>
        <!-- “管理”类快捷入口只对真正具备写权限的人展示。只读名册和模型列表
             仍可从「全部设置」进入，避免 viewer 看到名不副实的管理入口。
             客户视角额外隐藏这些后台入口，只保留个人设置与退出。 -->
        <div v-if="canManageMembers && !isCustomer" class="menu-item" @click="handleQuickNav('user-management')">
          <t-icon name="usergroup" class="menu-icon" />
          <span>{{ $t('tenantMember.title') }}</span>
        </div>
        <div v-if="canManageModels && !isCustomer" class="menu-item" @click="handleQuickNav('models')">
          <t-icon name="control-platform" class="menu-icon" />
          <span>{{ $t('settings.modelManagement') }}</span>
        </div>
        <div class="menu-divider"></div>
        <!-- 客户只保留「个人设置」；「全部设置」这个后台入口对客户隐藏（M2 / AC-12）。 -->
        <div v-if="!isCustomer" class="menu-item" @click="handleSettings">
          <t-icon name="setting" class="menu-icon" />
          <span>{{ $t('general.allSettings') }}</span>
        </div>
        <!--
          System administration entry — visible only to users with the
          platform-wide is_system_admin flag. Hidden for everyone else,
          including tenant Owners. Real authorisation lives server-side
          (RequireSystemAdmin middleware); this is UI gating only.
        -->
        <div v-if="authStore.isSystemAdmin" class="menu-item" @click="handleSystemAdmin">
          <t-icon name="server" class="menu-icon" />
          <span>{{ $t('settings.navGroups.systemAdministration') }}</span>
        </div>
        <!-- 帮助文档与 GitHub 都指向 WeKnora 外部资源，客户视角一起隐藏；
             数值/模型等后台入口已在上面单独收口。 -->
        <template v-if="!isCustomer">
          <div class="menu-divider"></div>
          <div class="menu-item" @click="openDocs">
            <t-icon name="help-circle" class="menu-icon" />
            <span class="menu-text-with-icon">
              <span>{{ $t('general.helpAndDocs') }}</span>
              <svg class="menu-external-icon" viewBox="0 0 16 16" aria-hidden="true">
                <path fill="currentColor"
                  d="M12.667 8a.667.667 0 0 1 .666.667v4a2.667 2.667 0 0 1-2.666 2.666H4.667a2.667 2.667 0 0 1-2.667-2.666V5.333a2.667 2.667 0 0 1 2.667-2.666h4a.667.667 0 1 1 0 1.333h-4a1.333 1.333 0 0 0-1.333 1.333v7.334A1.333 1.333 0 0 0 4.667 13.333h6a1.333 1.333 0 0 0 1.333-1.333v-4A.667.667 0 0 1 12.667 8Zm2.666-6.667v4a.667.667 0 0 1-1.333 0V3.276l-5.195 5.195a.667.667 0 0 1-.943-.943l5.195-5.195h-2.057a.667.667 0 0 1 0-1.333h4a.667.667 0 0 1 .666.666Z" />
              </svg>
            </span>
          </div>
          <div class="menu-item" :title="$t('common.githubStarTip')" @click="openGithub">
            <t-icon name="logo-github" class="menu-icon" />
            <span class="menu-text-with-icon">
              <span>{{ $t('common.github') }}</span>
              <t-icon name="star-filled" class="menu-github-star-icon" size="16px" aria-hidden="true" />
              <svg class="menu-external-icon" viewBox="0 0 16 16" aria-hidden="true">
                <path fill="currentColor"
                  d="M12.667 8a.667.667 0 0 1 .666.667v4a2.667 2.667 0 0 1-2.666 2.666H4.667a2.667 2.667 0 0 1-2.667-2.666V5.333a2.667 2.667 0 0 1 2.667-2.666h4a.667.667 0 1 1 0 1.333h-4a1.333 1.333 0 0 0-1.333 1.333v7.334A1.333 1.333 0 0 0 4.667 13.333h6a1.333 1.333 0 0 0 1.333-1.333v-4A.667.667 0 0 1 12.667 8Zm2.666-6.667v4a.667.667 0 0 1-1.333 0V3.276l-5.195 5.195a.667.667 0 0 1-.943-.943l5.195-5.195h-2.057a.667.667 0 0 1 0-1.333h4a.667.667 0 0 1 .666.666Z" />
              </svg>
            </span>
          </div>
        </template>
        <template v-if="!authStore.isLiteMode">
          <div class="menu-divider"></div>
          <div class="menu-item danger" @click="handleLogout">
            <t-icon name="logout" class="menu-icon" />
            <span>{{ $t('auth.logout') }}</span>
          </div>
        </template>
      </div>
    </Transition>

    <!-- 空间切换浮动面板（T10 抽成共享组件）：仅超管渲染，数据源 searchTenants
         （全部可访问空间）；hover 进出事件维持开合延时，行点击/键盘/触屏可达。 -->
    <TenantSwitchPanel :open="tenantSubmenuOpen" :anchor="tenantMenuItemRef ?? null"
      :super-admin="showTenantSwitcher" :current-tenant-id="authStore.effectiveTenantId"
      @pointer-enter="showTenantSubmenu" @pointer-leave="scheduleHideTenantSubmenu"
      @select="handlePanelSwitchAction" @created="handlePanelSwitchAction"
      @close="closeTenantSubmenu" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useCapabilitiesStore } from '@/stores/capabilities'
import { MessagePlugin } from 'tdesign-vue-next'
import { logout as logoutApi } from '@/api/auth'
import { useI18n } from 'vue-i18n'
import TenantSwitchPanel from '@/components/TenantSwitchPanel.vue'
import GlobalInvitationBell from '@/components/GlobalInvitationBell.vue'
import { useRoleLabel } from '@/composables/useRoleLabel'
import { openNewUserGuide } from '@/config/contextualGuides'
import { SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE } from '@/config/settingsAccess'
import { isCustomerViewer } from '@/config/customerAccess'
import { docsUrl } from '@/utils/docsUrl'
const { t } = useI18n()

const router = useRouter()
const uiStore = useUIStore()
const authStore = useAuthStore()
const capabilitiesStore = useCapabilitiesStore()
const { formatRole, roleIcon } = useRoleLabel()

// 顶部用户卡片展示的空间名 / 当前角色：跟着 tenant 切换器实时变。
// activeTenantName 优先用切换器选中的名字（含 fallback 到 home tenant 名字），
// 单空间用户也能正常显示自己的 home tenant 名。
const activeTenantName = computed(() => {
  return (
    authStore.selectedTenantName ||
    authStore.tenant?.name ||
    ''
  )
})
const currentRoleLabel = computed(() => formatRole(authStore.currentTenantRole))
const currentRoleIcon = computed(() => roleIcon(authStore.currentTenantRole))

// 客户视角隐藏后台类快捷入口（空间 / 成员 / 模型），只留个人设置与退出。
// 管理员保持原有菜单；这里只影响入口可见性（C4）。
const isCustomer = computed(() => isCustomerViewer(authStore))

// 单空间用户（memberships <= 1 且非 superuser）= 永远 home + owner，第三
// 行就是 user-email 信息的重复，没必要占视觉空间；只对多空间 / superuser
// 渲染。Lite 模式下没有 RBAC 概念，统一隐藏。
const showTenantIdentityLine = computed(() => {
  if (authStore.isLiteMode) return false
  if (authStore.canAccessAllTenants) return true
  return (authStore.memberships ?? []).length > 1
})

// 快捷入口使用“管理能力”而不是页面最低可见角色：成员名册和模型列表允许
// viewer 浏览，但头像菜单里的“管理”入口只服务实际能执行管理操作的角色。
// 「成员管理」现在直达用户管理页（owner+ / 系统管理员），旧 members 分区仍保留。
const canManageMembers = computed(() => capabilitiesStore.can('user.view'))
const canManageModels = computed(() =>
  authStore.canAccessAllTenants ||
  authStore.isSystemAdmin ||
  authStore.hasRole(SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE.models),
)
const menuRef = ref<HTMLElement>()
const tenantMenuItemRef = ref<HTMLElement>()
const menuVisible = ref(false)
const tenantSubmenuOpen = ref(false)
let tenantSubmenuHideTimer: ReturnType<typeof setTimeout> | null = null

// 用户信息直接读 auth store：启动时 main.ts 已用 /auth/me 校准过，
// 这里不再各自拉一份，避免首屏两次 /auth/me。
const userInfo = computed(() => ({
  username: authStore.user?.username || t('common.defaultUser'),
  email: authStore.user?.email || 'user@example.com',
  avatar: authStore.user?.avatar || '',
}))

const userName = computed(() => userInfo.value.username)
const userEmail = computed(() => userInfo.value.email)
const userAvatar = computed(() => userInfo.value.avatar)

// 用户名首字母（用于无头像时显示）
const userInitial = computed(() => {
  return userName.value.charAt(0).toUpperCase()
})

// 切换菜单显示
const toggleMenu = () => {
  menuVisible.value = !menuVisible.value
}

// 快捷导航到设置的特定部分
const handleQuickNav = (section: string) => {
  menuVisible.value = false
  uiStore.openSettings()
  router.push({ path: '/platform/settings', query: { section } })
}

// 打开设置
const handleSettings = () => {
  menuVisible.value = false
  uiStore.openSettings()
  router.push('/platform/settings')
}

// Open the platform administration group inside the standard Settings
// modal. Global settings is the group's landing page; task queues, platform
// API keys and the audit log remain available beside it in the settings nav.
const handleSystemAdmin = () => {
  menuVisible.value = false
  uiStore.openSettings('system-global')
  router.push({ path: '/platform/settings', query: { section: 'system-global' } })
}

// ---------- 空间切换入口 ----------
// 切换面板抽到 TenantSwitchPanel（T10）：这里只保留开合状态与 hover 延时，
// 定位 / searchTenants 列表 / 限流刷新 / 切换与创建链路都在面板组件内自理。
// 空间切换仅超管可用（AC-29）：普通用户单空间无切换语义；超管即使没有
// tenant_members 记录（memberships 为空）也能打开面板（AC-32）。
const showTenantSwitcher = computed(() => {
  return authStore.canAccessAllTenants || authStore.isSystemAdmin
})

// Hover 开合带一个小延时，容忍指针短暂滑过菜单项与面板之间的缝隙。
const showTenantSubmenu = () => {
  if (tenantSubmenuHideTimer) {
    clearTimeout(tenantSubmenuHideTimer)
    tenantSubmenuHideTimer = null
  }
  tenantSubmenuOpen.value = true
}

const closeTenantSubmenu = () => {
  if (tenantSubmenuHideTimer) {
    clearTimeout(tenantSubmenuHideTimer)
    tenantSubmenuHideTimer = null
  }
  tenantSubmenuOpen.value = false
}

const scheduleHideTenantSubmenu = () => {
  if (tenantSubmenuHideTimer) clearTimeout(tenantSubmenuHideTimer)
  tenantSubmenuHideTimer = setTimeout(closeTenantSubmenu, 180)
}

// 切换/创建已进入链路（随后整页刷新）：连同下拉菜单一起收起。
const handlePanelSwitchAction = () => {
  closeTenantSubmenu()
  menuVisible.value = false
}

const reopenGuide = () => {
  menuVisible.value = false
  openNewUserGuide()
}

const openDocs = () => {
  menuVisible.value = false
  window.open(docsUrl('home'), '_blank')
}

// 打开 GitHub
const openGithub = () => {
  menuVisible.value = false
  window.open('https://github.com/Tencent/WeKnora', '_blank')
}

// 注销
const handleLogout = async () => {
  menuVisible.value = false

  try {
    // 调用后端API注销
    await logoutApi()
  } catch (error) {
    // 即使API调用失败，也继续执行本地清理
    console.error('注销API调用失败:', error)
  }

  // 清理所有状态和本地存储
  authStore.logout()

  MessagePlugin.success(t('auth.logout'))

  // 跳转到登录页
  router.push('/login')
}

// 校准用户信息：本次会话已经用 /auth/me 校准过（main.ts 启动、登录流程）
// 就直接复用 store；只有自动初始化等没走过 /auth/me 的路径才会真的发请求。
// 落库逻辑（user / tenant / memberships / capabilities）统一在 auth store 里，
// 这里不再手写一份字段拷贝。
const loadUserInfo = async () => {
  try {
    await authStore.ensureAuthMe()
  } catch (error) {
    console.error('Failed to load user info:', error)
  }
}

// 点击外部关闭菜单
const handleClickOutside = (e: MouseEvent) => {
  const target = e.target as Node
  if (menuRef.value && menuRef.value.contains(target)) return
  // Tenant submenu is teleported to body, so it's not inside menuRef.
  const tenantFloating = document.querySelector('.tenant-submenu-floating')
  if (tenantFloating && tenantFloating.contains(target)) return
  menuVisible.value = false
  tenantSubmenuOpen.value = false
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  loadUserInfo()
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>

<style lang="less" scoped>
.user-menu {
  position: relative;
  width: 100%;

  &--collapsed {
    display: flex;
    flex-direction: column-reverse;
    align-items: center;
    gap: 4px;

    .user-bar {
      width: auto;
      justify-content: center;
    }

    .user-button {
      flex: none;
      justify-content: center;
      width: 32px;
      height: 32px;
      margin: 0 auto;
      padding: 0;
      gap: 0;
      border-radius: var(--app-radius-sm);
    }

    .user-dropdown {
      left: calc(100% + 8px);
      bottom: 0;
      right: auto;
      /* 与展开侧栏时下拉可视宽度对齐（aside 宽 260px） */
      min-width: 260px;
    }
  }
}

.user-bar {
  display: flex;
  align-items: center;
  gap: 2px;
  width: 100%;
  min-width: 0;
}

.user-button {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 6px;
  min-width: 0;
  padding: 8px 6px;
  border-radius: var(--app-radius-md);
  cursor: pointer;
  transition: background-color var(--app-motion-base);
  background: transparent;

  &:hover {
    background: var(--td-bg-color-container-hover);
  }

  &:active {
    transform: scale(0.98);
  }
}

.user-avatar {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  overflow: hidden;
  flex-shrink: 0;
  background: linear-gradient(135deg, var(--td-brand-color) 0%, var(--td-brand-color-active) 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: width var(--app-motion-base) ease, height var(--app-motion-base) ease;

  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }

  .avatar-placeholder {
    color: var(--td-text-color-anti);
    font-size: var(--app-text-sm);
    font-weight: 600;
    line-height: 1;
  }
}

.user-info {
  flex: 1;
  min-width: 0;
  text-align: left;
  display: flex;
  flex-direction: column;
  gap: 2px;
  justify-content: center;

  .user-name {
    font-size: var(--app-text-base);
    font-weight: 500;
    color: var(--td-text-color-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .user-email {
    font-size: var(--app-text-sm);
    color: var(--td-text-color-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .user-tenant-name {
    font-size: var(--app-text-base);
    font-weight: 600;
    letter-spacing: -0.01em;
    color: var(--td-text-color-primary);
    line-height: 1.35;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .user-tenant-meta {
    display: flex;
    align-items: center;
    gap: 4px;
    margin-top: 0;
    min-width: 0;
    font-size: var(--app-text-sm);
    line-height: 1.35;
    color: var(--td-text-color-secondary);

    .user-tenant-meta-name {
      flex: 0 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .user-tenant-meta-sep {
      flex-shrink: 0;
      color: var(--td-text-color-placeholder);
    }

    .user-tenant-meta-icon {
      flex-shrink: 0;
      color: inherit;
    }

    .user-tenant-meta-role {
      flex-shrink: 0;
    }
  }
}

.user-notify {
  display: inline-flex;
  flex-shrink: 0;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 0;
  border-radius: var(--app-radius-sm);
  background: transparent;
  color: var(--td-text-color-placeholder);
  cursor: pointer;

  &:hover,
  &.is-open {
    background: var(--td-bg-color-container-hover);
    color: var(--td-text-color-primary);
  }
}

.user-notify__dot {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--td-error-color);
  box-shadow: 0 0 0 1.5px var(--td-bg-color-sidebar);
}

.user-menu--collapsed .user-notify {
  position: relative;
}

.user-dropdown {
  position: absolute;
  bottom: 100%;
  /* 相对 .user-menu：左右由 left/right 拉宽；右缘用正值内缩，避免与侧栏内容区右边界完全重合 */
  left: -4px;
  right: -5px;
  margin-bottom: 6px;
  background: var(--td-bg-color-container);
  border-radius: var(--app-radius-md);
  box-shadow: 0 4px 20px rgba(0, 0, 0, 0.12);
  border: 1px solid var(--td-component-stroke);
  overflow: hidden;
  z-index: 1000;
}

// 下拉顶部 — 账号区：24px 头像中心与下方 16px 菜单图标中心同竖线；
// margin-left −4px、gap 6px 保持昵称起点与菜单文案对齐（12 + 24 + 6 − 4 = 38）
.dropdown-user-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 9px 12px;
  min-width: 0;

  &.is-clickable {
    cursor: pointer;
    transition: background-color var(--app-motion-fast) ease;

    &:hover,
    &:focus-visible {
      background: var(--td-bg-color-container-hover);
      outline: none;
    }
  }

  .dropdown-user-avatar {
    width: 24px;
    height: 24px;
    margin-left: -4px;
    border-radius: 50%;
    overflow: hidden;
    flex-shrink: 0;
    background: linear-gradient(135deg, var(--td-brand-color) 0%, var(--td-brand-color-active) 100%);
    display: flex;
    align-items: center;
    justify-content: center;

    img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }

    .dropdown-user-avatar-placeholder {
      color: var(--td-text-color-anti);
      font-size: var(--app-text-sm);
      font-weight: 600;
      line-height: 1;
    }
  }

  .dropdown-user-meta {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0;
    justify-content: center;
  }

  .dropdown-user-name-row {
    display: flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
  }

  .dropdown-user-name {
    flex: 1;
    min-width: 0;
    font-size: var(--app-text-base);
    font-weight: 500;
    color: var(--td-text-color-primary);
    line-height: 1.35;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .dropdown-user-email {
    min-width: 0;
    font-size: var(--app-text-sm);
    line-height: 1.35;
    color: var(--td-text-color-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .dropdown-guide-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 20px;
    height: 20px;
    margin: 0;
    padding: 0;
    border: none;
    border-radius: var(--app-radius-xs);
    background: transparent;
    color: var(--td-text-color-placeholder);
    cursor: pointer;
    transition: background-color var(--app-motion-base) ease, color var(--app-motion-base) ease;

    &:hover {
      background: var(--td-bg-color-container-hover);
      color: var(--td-text-color-secondary);
    }
  }
}

// 下拉 — 当前工作区：与下方 .menu-item 同款对齐（左 16px 图标槽 + 文案列 + 右侧操作图标）
.dropdown-tenant-panel {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border-top: 1px solid var(--td-component-stroke);
  background: transparent;
  transition: background var(--app-motion-fast) ease;
  min-width: 0;

  >.menu-icon {
    font-size: var(--app-text-xl);
    color: var(--td-text-color-secondary);
    flex-shrink: 0;
  }

  &.is-clickable {
    cursor: pointer;

    &:hover,
    &.is-open {
      background: var(--td-bg-color-container-hover);

      .dropdown-tenant-panel-trail {
        color: var(--td-text-color-secondary);
      }
    }
  }

  .dropdown-tenant-panel-main {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }

  .dropdown-tenant-panel-name {
    font-size: var(--app-text-base);
    font-weight: 500;
    color: var(--td-text-color-primary);
    line-height: 1.35;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .dropdown-tenant-panel-trail {
    flex-shrink: 0;
    font-size: var(--app-text-xl);
    color: var(--td-text-color-placeholder);
    transition: color var(--app-motion-fast) ease;
  }

  .dropdown-tenant-panel-role {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: var(--app-text-sm);
    line-height: 1.35;
    color: var(--td-text-color-secondary);
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;

    .dropdown-tenant-panel-role-icon {
      flex-shrink: 0;
      color: inherit;
    }
  }
}

.menu-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  cursor: pointer;
  transition: all var(--app-motion-base);
  font-size: var(--app-text-base);
  color: var(--td-text-color-primary);

  &:hover {
    background: var(--td-bg-color-container-hover);
  }

  &.danger {
    color: var(--td-error-color);

    &:hover {
      background: var(--td-error-color-light);
    }

    .menu-icon {
      color: var(--td-error-color);
    }
  }

  // 包含右弹子菜单的菜单项
  &--submenu {
    position: relative;

    .menu-item-label {
      flex: 1;
    }

    .menu-chevron {
      font-size: var(--app-text-xl);
      color: var(--td-text-color-placeholder);
      flex-shrink: 0;
      transition: transform var(--app-motion-fast);
    }

    &.is-open {
      background: var(--td-bg-color-container-hover);

      .menu-chevron {
        color: var(--td-text-color-secondary);
      }
    }
  }

  .menu-icon {
    font-size: var(--app-text-xl);
    color: var(--td-text-color-secondary);

    &.svg-icon {
      width: 16px;
      height: 16px;
      flex-shrink: 0;
    }

    &--emoji {
      width: 16px;
      height: 16px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      font-size: var(--app-text-lg);
      line-height: 1;
      flex-shrink: 0;
      color: inherit;
    }
  }

  .menu-text-with-icon {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 6px;
    color: inherit;
    min-width: 0;

    >span:first-of-type {
      display: inline-flex;
      align-items: center;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
  }

  .menu-new-badge {
    flex-shrink: 0;
    font-size: var(--app-text-2xs);
    font-weight: 600;
    line-height: 1.2;
    padding: 2px 5px;
    border-radius: var(--app-radius-xs);
    background: var(--td-brand-color-light);
    color: var(--td-brand-color);
    letter-spacing: 0.02em;
  }

  .menu-github-star-icon {
    flex-shrink: 0;
    color: var(--td-warning-color);
  }

  .menu-external-icon {
    width: 16px;
    height: 16px;
    color: var(--td-text-color-disabled);
    flex-shrink: 0;
    transition: color var(--app-motion-base) ease;
    pointer-events: none;
  }

  &:hover .menu-external-icon {
    color: var(--td-brand-color);
  }
}

.menu-divider {
  height: 1px;
  background: var(--td-component-stroke);
  margin: 3px 0;
}

// 紧跟账号/空间区块后的分隔线：略收紧与上方的留白
.dropdown-user-header+.menu-divider,
.dropdown-tenant-panel+.menu-divider {
  margin-top: 1px;
}

// 下拉动画
.dropdown-enter-active,
.dropdown-leave-active {
  transition: all var(--app-motion-base) cubic-bezier(0.4, 0, 0.2, 1);
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: translateY(8px);
}

.dropdown-enter-to,
.dropdown-leave-from {
  opacity: 1;
  transform: translateY(0);
}

</style>
