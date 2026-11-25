/**
 * T10：同步面板的挂载位置与权限门控（C6 例外 + C4）。
 *
 * 票面要求：只挂在「用户管理」分区、仅系统管理员可见、不新增侧边栏菜单，
 * 也不恢复 Q31 已迁出的旧 users 区。这里用源码断言钉住挂载点；
 * 真正的权限隔离在后端逐接口校验（T08 已覆盖），前端隐藏不是隔离。
 */
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const FRONTEND_SRC = new URL('../../', import.meta.url)
const SETTINGS = new URL('./Settings.vue', import.meta.url)
const SYSTEM_SETTINGS = new URL('../system/SystemSettings.vue', import.meta.url)
const ROUTER = new URL('../../router/index.ts', import.meta.url)

const settingsSource = readFileSync(SETTINGS, 'utf8')

test('the panel is mounted inside the user-management section, right after UserManagement', () => {
  const section = settingsSource.match(
    /<div v-if="currentSection === 'user-management'" class="section">([\s\S]*?)<\/div>/,
  )
  assert.ok(section, 'user-management section must exist')
  const body = section![1]!
  const userManagementAt = body.indexOf('<UserManagement />')
  const panelAt = body.indexOf('<YunzhijiaSyncPanel')
  assert.ok(userManagementAt >= 0, 'UserManagement must stay in the section')
  assert.ok(panelAt > userManagementAt, 'panel must follow UserManagement')
  assert.match(
    body,
    /<YunzhijiaSyncPanel v-if="authStore\.isSystemAdmin" \/>/,
    'panel is gated by authStore.isSystemAdmin',
  )
})

test('the panel is mounted exactly once and nowhere else in the app', () => {
  const mounts = settingsSource.match(/<YunzhijiaSyncPanel\b/g) ?? []
  assert.equal(mounts.length, 1, 'exactly one mount point in Settings.vue')

  const srcRoot = fileURLToPath(FRONTEND_SRC)
  const offenders: string[] = []
  for (const entry of readdirSync(srcRoot, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile() || !/\.(vue|ts)$/.test(entry.name) || /\.test\.(ts|mjs)$/.test(entry.name)) continue
    const path = `${entry.parentPath}/${entry.name}`
    if (path.endsWith('YunzhijiaSyncPanel.vue')) continue
    if (readFileSync(path, 'utf8').includes('YunzhijiaSyncPanel')) offenders.push(path)
  }
  const relative = offenders.map((path) => path.slice(srcRoot.length).replaceAll('\\', '/')).sort()
  assert.deepEqual(relative, ['views/settings/Settings.vue'], 'only Settings.vue may import the panel')
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
