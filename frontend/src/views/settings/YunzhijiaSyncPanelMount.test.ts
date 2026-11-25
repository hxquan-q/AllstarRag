/**
 * T10 / H2：同步面板的挂载位置与权限门控（C6 例外 + C4）。
 *
 * 用户反馈：面板放在用户表格下方不合理，改为与「成员列表 / 邀请管理 /
 * 操作记录」同一排的第四个 Tab。这里用源码断言钉住：
 * - `UserManagement.vue` 有第 4 个 `t-tab-panel value="sync"`，仅 `isSystemAdmin`；
 * - 面板内容在 sync Tab 内，受 `isSystemAdmin && activeTab === 'sync'` 门控，
 *   并向面板传 `:tenant-id="tenantId"`；
 * - `Settings.vue` 不再 import/挂载面板，H1 的 `#footer` 插槽已删除；
 * - 全应用唯一挂载、不新增菜单/路由、不恢复 SystemSettings 旧 users 区。
 *
 * 真正的权限隔离仍由后端逐接口校验（T08 已覆盖），前端隐藏不是隔离。
 */
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const FRONTEND_SRC = new URL('../../', import.meta.url)
const SETTINGS = new URL('./Settings.vue', import.meta.url)
const USER_MANAGEMENT = new URL('../user-management/UserManagement.vue', import.meta.url)
const SYSTEM_SETTINGS = new URL('../system/SystemSettings.vue', import.meta.url)
const ROUTER = new URL('../../router/index.ts', import.meta.url)

const settingsSource = readFileSync(SETTINGS, 'utf8')
const userManagementSource = readFileSync(USER_MANAGEMENT, 'utf8')

test('UserManagement renders the sync panel as a system-admin-only fourth tab (H2)', () => {
  // 四个 Tab：成员列表 / 邀请管理 / 操作记录 / 云之家同步。
  const panels = userManagementSource.match(/<t-tab-panel\b/g) ?? []
  assert.equal(panels.length, 4, '用户管理应有 4 个 Tab，不新增菜单项')

  const syncTab = userManagementSource.match(/<t-tab-panel\b[^>]*value="sync"[^>]*\/>/)
  assert.ok(syncTab, 'sync Tab 必须存在')
  assert.match(syncTab![0], /v-if="isSystemAdmin"/, 'sync Tab 仅系统管理员可见')
  assert.match(
    syncTab![0],
    /:label="t\('system\.globalSettings\.yunzhijiaSync\.title'\)"/,
    'Tab 标题复用现有 i18n 键，不新增标题键',
  )

  // 面板内容在 Tab 内，未选中不挂载（直接 v-if，无 KeepAlive）。
  const mount = userManagementSource.match(/<YunzhijiaSyncPanel\b[\s\S]*?\/>/)
  assert.ok(mount, 'UserManagement 必须挂载同步面板')
  assert.match(
    mount![0],
    /v-if="isSystemAdmin && activeTab === 'sync'"/,
    '面板受系统管理员与 sync Tab 双重门控',
  )
  assert.match(mount![0], /:tenant-id="tenantId"/, '面板接收当前空间 tenantId')
  // H2 收尾：去掉 KeepAlive，每次进入 sync Tab 重新挂载并拉取最新状态。
  assert.doesNotMatch(
    userManagementSource,
    /<KeepAlive>\s*<YunzhijiaSyncPanel\b/,
    '面板不得再用 KeepAlive 包裹',
  )
  assert.match(
    userManagementSource,
    /<\/KeepAlive>\s*<!--[^>]*-->\s*<YunzhijiaSyncPanel\b/,
    '面板直接挂在操作记录 Tab 的 KeepAlive 之后，未选中时不挂载',
  )
})

test('UserManagement no longer exposes the H1 footer slot (H2)', () => {
  assert.doesNotMatch(userManagementSource, /<slot\b[^>]*name="footer"/)
  assert.doesNotMatch(userManagementSource, /<\/slot>/)
})

test('Settings no longer imports or mounts the YunzhijiaSyncPanel (H2)', () => {
  assert.doesNotMatch(settingsSource, /YunzhijiaSyncPanel/, 'Settings.vue 不得再引用面板')
  assert.doesNotMatch(settingsSource, /#footer/, 'H1 的 #footer 插槽必须移除')
  // user-management 分区仍保留，且以自闭合方式渲染页面。
  assert.match(
    settingsSource,
    /currentSection === 'user-management'[\s\S]*?<UserManagement\s*\/>/,
    'UserManagement 仍挂在用户管理分区，且不再传插槽',
  )
})

test('the panel is mounted exactly once and nowhere else in the app', () => {
  const mounts = userManagementSource.match(/<YunzhijiaSyncPanel\b/g) ?? []
  assert.equal(mounts.length, 1, 'exactly one mount point in UserManagement.vue')

  const srcRoot = fileURLToPath(FRONTEND_SRC)
  const offenders: string[] = []
  for (const entry of readdirSync(srcRoot, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile() || !/\.(vue|ts)$/.test(entry.name) || /\.test\.(ts|mjs)$/.test(entry.name)) continue
    const path = `${entry.parentPath}/${entry.name}`
    if (path.endsWith('YunzhijiaSyncPanel.vue')) continue
    if (readFileSync(path, 'utf8').includes('YunzhijiaSyncPanel')) offenders.push(path)
  }
  const relative = offenders.map((path) => path.slice(srcRoot.length).replaceAll('\\', '/')).sort()
  assert.deepEqual(relative, ['views/user-management/UserManagement.vue'], 'only UserManagement.vue may import the panel')
})

test('no sidebar menu or route entry was added for the sync panel', () => {
  // 侧边栏分区来自 Settings.vue 的 navItems；不得出现 yunzhijia 菜单项。
  assert.doesNotMatch(settingsSource, /key:\s*['"][^'"]*yunzhijia/i)
  assert.doesNotMatch(settingsSource, /icon:\s*['"][^'"]*yunzhijia/i)

  const routerSource = readFileSync(ROUTER, 'utf8')
  assert.doesNotMatch(routerSource, /yunzhijia/i, '不得为同步面板新增路由')
})

test('the Q31-retired users area in SystemSettings.vue is not restored', () => {
  const systemSettingsSource = readFileSync(SYSTEM_SETTINGS, 'utf8')
  assert.doesNotMatch(systemSettingsSource, /YunzhijiaSyncPanel/, '不得把面板挂回 SystemSettings 旧 users 区')
})
