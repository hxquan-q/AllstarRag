import type { DeploymentCapabilityKey } from './deploymentCapabilities'

export const CHROME_EXTENSION_URL =
  'https://chromewebstore.google.com/detail/jpemjbopikggjlmikmclgbmkhhopjdgd?utm_source=item-share-cb'

// T03（admin-feature-surface）：claw 已按裁定清单全角色删除（外链页 + 导航项）。
// 'claw' 仍保留在 IntegrationTab / INTEGRATION_TABS 里：settingsRoute 的
// integration-claw 映射靠它把旧 deep-link ?section=integration-claw 归一化，
// 再由 Settings.vue canSeeSection 落到 role-denied 兜底，不能一并删。
export type IntegrationTab = 'im' | 'embed' | 'api' | 'mcpserver' | 'cli' | 'chrome' | 'claw'

export const INTEGRATION_TABS: IntegrationTab[] = ['im', 'embed', 'api', 'mcpserver', 'cli', 'chrome', 'claw']

/** Aligns with Settings.vue SECTION_MIN_ROLE.api and router.go g.Owner() on /api-principal-config. */
export type IntegrationTabRole = 'viewer' | 'contributor' | 'admin' | 'owner'

export const INTEGRATION_TAB_MIN_ROLE: Partial<Record<IntegrationTab, IntegrationTabRole>> = {
  api: 'owner',
}

export const INTEGRATION_TAB_CAPABILITY: Partial<Record<IntegrationTab, DeploymentCapabilityKey>> = {
  im: 'integrations.im',
  embed: 'integrations.embed',
  api: 'integrations.api',
  mcpserver: 'integrations.mcpserver',
}

export type IntegrationPreviewIcon = { type: 'icon'; name: string }

/** Sidebar hover preview + Integrations modal nav — add new entries here. */
export const INTEGRATION_PREVIEW_ITEMS: Array<{
  key: IntegrationTab
  icon: IntegrationPreviewIcon
}> = [
  { key: 'im', icon: { type: 'icon', name: 'chat-message' } },
  { key: 'embed', icon: { type: 'icon', name: 'code' } },
  { key: 'api', icon: { type: 'icon', name: 'secured' } },
  { key: 'mcpserver', icon: { type: 'icon', name: 'tools' } },
  { key: 'cli', icon: { type: 'icon', name: 'code' } },
  { key: 'chrome', icon: { type: 'icon', name: 'extension' } },
]
