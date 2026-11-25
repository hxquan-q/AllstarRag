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
const USER_MANAGEMENT = new URL('../user-management/UserManagement.vue', import.meta.url)
const SYSTEM_SETTINGS = new URL('../system/SystemSettings.vue', import.meta.url)
const ROUTER = new URL('../../router/index.ts', import.meta.url)

const settingsSource = readFileSync(SETTINGS, 'utf8')
const userManagementSource = readFileSync(USER_MANAGEMENT, 'utf8')

/**
 * 找到 `openAt` 处 `<div>` 的配对 `</div>` 位置。只数 div 开闭标签，
 * 用于确认插槽确实落在根 `.user-management` 内部而不是 root 之后/之外。
 */
function findMatchingDivClose(source: string, openAt: number): number {
  const tag = /<div\b|<\/div>/g
  tag.lastIndex = openAt
  let depth = 0
  for (let match = tag.exec(source); match !== null; match = tag.exec(source)) {
    if (match[0] === '</div>') {
      depth -= 1
      if (depth === 0) return match.index
    } else {
      depth += 1
    }
  }
  return -1
}

test('the panel is mounted through the UserManagement #footer slot (H1)', () => {
  const section = settingsSource.match(
    /<div v-if="currentSection === 'user-management'" class="section">([\s\S]*?)<\/div>/,
  )
  assert.ok(section, 'user-management section must exist')
  const body = section![1]!
  const userManagement = body.match(/<UserManagement\b[^>]*>([\s\S]*?)<\/UserManagement>/)
  assert.ok(userManagement, 'UserManagement must stay in the section')
  const inner = userManagement![1]!
  const footer = inner.match(/<template #footer>([\s\S]*?)<\/template>/)
  assert.ok(footer, 'UserManagement must receive a #footer slot')
  assert.match(
    footer![1]!,
    /<YunzhijiaSyncPanel v-if="authStore\.isSystemAdmin" \/>/,
    'panel is mounted in #footer and gated by authStore.isSystemAdmin',
  )
  // 面板不得再作为 UserManagement 的兄弟节点出现在分区里（H1 根因）。
  assert.doesNotMatch(
    body.replace(userManagement![0], ''),
    /<YunzhijiaSyncPanel\b/,
    'panel must not be mounted outside UserManagement',
  )
})

test('UserManagement.vue forwards the footer slot inside the .user-management root (H1)', () => {
  const template = userManagementSource.match(/<template>([\s\S]*)<\/template>/)
  assert.ok(template, 'UserManagement.vue must have a template block')
  const source = template![1]!

  const slotAt = source.indexOf('<slot name="footer" />')
  assert.ok(slotAt >= 0, 'UserManagement.vue must declare <slot name="footer" />')

  const rootAt = source.indexOf('<div class="user-management">')
  assert.ok(rootAt >= 0, 'root .user-management element must exist')
  const rootCloseAt = findMatchingDivClose(source, rootAt)
  assert.ok(rootCloseAt > rootAt, 'root .user-management element must be balanced')
  assert.ok(
    slotAt > rootAt && slotAt < rootCloseAt,
    'footer slot must live inside the .user-management root (not after/outside it)',
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
