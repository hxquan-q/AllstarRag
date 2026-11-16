import { navigateAfterTenantSwitch, persistLastActiveTenantPreference } from '@/utils/tenantSwitch'
import type { TenantSwitchTarget } from './types'

/**
 * 多空间视图的「切换到该空间」（Q44）。
 *
 * 复用租户切换器现有的等价能力：写入选中空间 → 持久化 last_active 偏好 →
 * 整页刷新。整页刷新会让所有 store / SSE / 飞行中请求按新空间重建，
 * 避免用旧空间的缓存看错数据。
 */
export interface TenantSwitchAuth {
  user?: { tenant_id?: string | number | null } | null
  setSelectedTenant(tenantId: number | null, tenantName?: string | null): void
}

export function switchToTargetTenant(
  auth: TenantSwitchAuth,
  target: TenantSwitchTarget,
): void {
  const homeTenantId = auth.user?.tenant_id ? Number(auth.user.tenant_id) : null
  const switchingToHome =
    homeTenantId !== null && Number.isFinite(homeTenantId) && homeTenantId === target.tenantId
  auth.setSelectedTenant(target.tenantId, target.tenantName)
  // 与 TenantSelector / UserMenu 一致：切回 home 时清空偏好，其余记录目标空间。
  const persist = persistLastActiveTenantPreference(switchingToHome ? null : target.tenantId)
  Promise.race([persist, new Promise<void>((resolve) => setTimeout(resolve, 400))])
    .finally(() => navigateAfterTenantSwitch())
}
