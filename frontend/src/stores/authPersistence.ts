import { userInfoFromApi, type LoginResponse } from '@/api/auth'
import type { useAuthStore } from '@/stores/auth'

type AuthStore = ReturnType<typeof useAuthStore>

// 密码登录与云之家 ticket 启动登录共用同一份会话写入逻辑（T09），
// 避免两套字段白名单漂移。放在独立文件，由 stores/auth.ts 统一导出，
// 登录页与启动流程都从 store 模块引用。
export function persistLoginResponseToStore(authStore: AuthStore, response: LoginResponse) {
  const activeTenant = response.active_tenant || response.tenant
  if (!response.user || !response.token) return

  // user.tenant_id 必须是用户的 HOME 空间（不可变）；当前生效空间
  // 通过 setSelectedTenant 单独表达。
  const homeTenantIdRaw = response.user.tenant_id ?? activeTenant?.id ?? ''
  authStore.setUser(userInfoFromApi(response.user, homeTenantIdRaw))
  authStore.setToken(response.token)
  if (response.refresh_token) {
    authStore.setRefreshToken(response.refresh_token)
  }
  if (activeTenant) {
    authStore.setTenant({
      id: String(activeTenant.id) || '',
      name: activeTenant.name || '',
      owner_id: response.user.id || '',
      created_at: activeTenant.created_at || new Date().toISOString(),
      updated_at: activeTenant.updated_at || new Date().toISOString(),
    })
  } else {
    authStore.setTenant(null)
  }
  if (Array.isArray(response.memberships)) {
    authStore.setMemberships(response.memberships)
  }
  // 登录响应携带能力投影，引导页在 /auth/me 返回前就能隐藏创建/邀请入口。
  const capabilities = response.capabilities
  if (typeof capabilities?.can_create_tenant === 'boolean') {
    authStore.setCanCreateTenant(capabilities.can_create_tenant)
  }
  if (typeof capabilities?.access_revoked === 'boolean') {
    authStore.setAccessRevoked(capabilities.access_revoked)
  }
  // 后端若按上次活跃空间登录，设置覆盖；否则清掉上一个账号残留。
  const activeIdNum = Number(activeTenant?.id)
  const homeIdNum = Number(homeTenantIdRaw)
  if (Number.isFinite(activeIdNum) && Number.isFinite(homeIdNum) && activeIdNum !== homeIdNum) {
    authStore.setSelectedTenant(activeIdNum, activeTenant?.name || null)
  } else {
    authStore.setSelectedTenant(null, null)
  }
}
