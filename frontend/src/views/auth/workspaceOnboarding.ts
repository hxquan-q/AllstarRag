/**
 * WorkspaceOnboarding 的可见分支判定。
 *
 * 被移出所有空间(accessRevoked)时只保留退出登录:隐藏「创建空间」与
 * 「查看邀请」,由组件展示提示卡片。正常用户完全沿用既有分支:
 * 创建按钮仍由 canCreateTenant 决定,邀请入口始终可见。
 *
 * 抽成纯函数方便单测(node:test 环境不挂载 Vue 组件)。
 */
export interface OnboardingSurfaces {
  showCreate: boolean
  showInvitations: boolean
  showAccessRevoked: boolean
}

export function resolveOnboardingSurfaces(
  canCreateTenant: boolean,
  accessRevoked: boolean,
): OnboardingSurfaces {
  if (accessRevoked) {
    return { showCreate: false, showInvitations: false, showAccessRevoked: true }
  }
  return {
    showCreate: canCreateTenant === true,
    showInvitations: true,
    showAccessRevoked: false,
  }
}
