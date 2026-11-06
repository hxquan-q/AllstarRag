import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { CUSTOMER_SETTINGS_SECTIONS } from '../../config/customerAccess.ts'
import {
  SETTINGS_SECTION_MIN_ROLE,
  SYSTEM_ADMIN_SETTINGS_SECTIONS,
} from '../../config/settingsAccess.ts'

/**
 * 客户个人设置（AC-11 / AC-23 / AC-24 / C6）：
 * - 客户只看到 general / userprofile / mymemory / agentlines 四项（H2 后助手分区也对客户隐藏）；
 * - 管理员 25 个基础分区（原 23 + 上游 #4822 的 defaults + 本 feature 的 agentlines）中
 *   不出现 agentlines，其余完全不受影响（另有集成分区，不在本用例）；
 * - deep-link 非法 section 由路由归一化到 general（customerAccess.test.ts 已覆盖）。
 *
 * 基数是合入上游后的实测值：23 + defaults + agentlines = 25，再加上本 feature 的
 * user-management = 26。
 * 管理员侧不渲染 agentlines（C6）；user-management 入口自 CP3 修复轮 1（F14）起
 * 只认能力投影 user.view（AC-12b），因此普通 admin/跨空间超管（投影为空）恒为 19、
 * owner（投影含 user.view）与系统管理员（全量投影）可见该分区。
 */

const settingsSource = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')
const script = settingsSource.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)![1]!
const ast = ts.createSourceFile('Settings.ts', script, ts.ScriptTarget.Latest, true)

function findStatementText(match: (statement: ts.Statement) => boolean, label: string): string {
  const statement = ast.statements.find(match)
  assert.ok(statement, `Settings.vue must define ${label}`)
  return statement.getText(ast)
}

const navItemsText = findStatementText(
  (statement) =>
    ts.isVariableStatement(statement) &&
    statement.declarationList.declarations.some(
      (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'navItems',
    ),
  'navItems',
)
const canSeeSectionText = ts.transpileModule(
  findStatementText(
    (statement) =>
      ts.isVariableStatement(statement) &&
      statement.declarationList.declarations.some(
        (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'canSeeSection',
      ),
    'canSeeSection',
  ).replace(/^const canSeeSection = /, ''),
  { compilerOptions: { target: ts.ScriptTarget.ES2022 } },
).outputText

// navItems 里 `const all: NavItem[] = [ ... ]` 的基础分区 key（不包含集成分区）。
const allStart = navItemsText.indexOf('const all: NavItem[] = [')
const allEnd = navItemsText.indexOf('...integrationItems', allStart)
assert.notEqual(allStart, -1)
assert.notEqual(allEnd, -1)
const allBlock = navItemsText.slice(allStart, allEnd)
const baseKeys = [...allBlock.matchAll(/\{\s*key:\s*'([^']+)'/g)].map((match) => match[1]!)
assert.equal(new Set(baseKeys).size, baseKeys.length, 'base settings keys must be unique')

// CP3 修复轮 1（F14）：user-management 入口只认 GET /auth/capabilities 的能力投影。
// 这里镜像后端唯一事实源 internal/types/capabilities.go 的矩阵：
// 系统管理员 → 全量；owner → owner 集合（含 user.view）；
// admin/contributor/viewer（非系统管理员，含跨空间超管的当前空间角色）→ 空。
const OWNER_CAPS = [
  'user.view', 'user.invite', 'user.create', 'user.role.change',
  'user.ban', 'user.unban', 'user.purge', 'user.owner.add', 'space.transfer',
]
const SYSTEM_ADMIN_CAPS = [...OWNER_CAPS, 'user.owner.remove', 'owner.provision']

function visibleFor(
  role: 'viewer' | 'contributor' | 'admin' | 'owner',
  options: { systemAdmin?: boolean; crossTenant?: boolean; caps?: readonly string[] } = {},
) {
  const auth = {
    role,
    isSystemAdmin: options.systemAdmin ?? false,
    canAccessAllTenants: options.crossTenant ?? false,
    hasRole: (min: string) => {
      const levels: Record<string, number> = { viewer: 1, contributor: 2, admin: 3, owner: 4 }
      return (levels[role] ?? 0) >= (levels[min] ?? 0)
    },
  }
  const projected = options.caps
    ?? (options.systemAdmin ? SYSTEM_ADMIN_CAPS : role === 'owner' ? OWNER_CAPS : [])
  const caps = new Set(projected)
  // 重新执行以注入当前 authStore / capabilitiesStore（canSeeSection 闭包引用两者）
  const fn = runInNewContext(
    canSeeSectionText,
    {
      isCustomerViewer: (store: { role: string }) => store.role === 'viewer',
      isCustomerSettingsSection: (key: string) => CUSTOMER_SETTINGS_SECTIONS.includes(key),
      isIntegrationSection: () => false,
      INTEGRATION_TAB_MIN_ROLE: {},
      integrationTabFromSection: () => 'integrations',
      SYSTEM_ADMIN_SECTIONS: SYSTEM_ADMIN_SETTINGS_SECTIONS,
      SETTINGS_SECTION_MIN_ROLE,
      authStore: auth,
      capabilitiesStore: { can: (capability: string) => caps.has(capability) },
    },
  ) as never as (key: string) => boolean
  return baseKeys.filter((key) => fn(key))
}

test('settings navigation has 26 non-integration sections (31 with integrations)', () => {
  assert.equal(baseKeys.length, 26, `base settings keys: ${baseKeys.join(', ')}`)
  assert.deepEqual(baseKeys.slice(0, 3), ['general', 'ollama', 'weknoracloud'])
  assert.ok(baseKeys.includes('userprofile'))
  assert.ok(baseKeys.includes('assistant'))
  assert.ok(baseKeys.includes('mymemory'))
  assert.ok(baseKeys.includes('envvars'))
  assert.ok(baseKeys.includes('agentlines'), 'T03 adds the agent lines section')
  assert.ok(baseKeys.includes('user-management'), 'T09 adds the user management section')
})

test('customer sees exactly the four allowed sections, in the account group order', () => {
  assert.deepEqual(visibleFor('viewer'), ['general', 'userprofile', 'mymemory', 'agentlines'])
  // 分组顺序仍由 navGroups 的 account 组决定；助手虽在分组列表里，但会被客户过滤掉。
  const accountGroup = settingsSource.match(/pickItems\(\[('general'[^\]]*)\]\)/)?.[1] ?? ''
  assert.deepEqual(
    [...accountGroup.matchAll(/'([^']+)'/g)].map((match) => match[1]),
    ['general', 'userprofile', 'assistant', 'mymemory', 'envvars', 'agentlines'],
  )
  assert.match(settingsSource, /currentSection === 'assistant' && !isCustomerViewerActive/)
})

test('customer settings whitelist and assistant section follow H2/AC-23', () => {
  assert.deepEqual([...CUSTOMER_SETTINGS_SECTIONS], ['general', 'userprofile', 'mymemory', 'agentlines'])
  assert.equal(CUSTOMER_SETTINGS_SECTIONS.includes('assistant'), false)
})

test('agentlines section is customer-only: hidden from admin, rendered only for the customer view (C6)', () => {
  assert.equal(visibleFor('viewer').includes('agentlines'), true)
  assert.equal(visibleFor('admin').includes('agentlines'), false)
  assert.equal(visibleFor('owner').includes('agentlines'), false)
  assert.equal(visibleFor('admin', { systemAdmin: true }).includes('agentlines'), false)
  // 渲染分支双保险：只有客户视角才挂 AgentLineSettings。
  assert.match(
    settingsSource,
    /currentSection === 'agentlines' && isCustomerViewerActive[\s\S]*?<AgentLineSettings \/>/,
  )
})

test('customer hidden sections are all filtered out', () => {
  const visible = visibleFor('viewer')
  for (const hidden of [
    'models', 'ollama', 'vectorstore', 'parser', 'storage', 'websearch', 'chathistory',
    'memory', 'mcp', 'browserconnection', 'members', 'tenant', 'system', 'system-global',
    'sandbox', 'skills', 'envvars', 'weknoracloud',
  ]) {
    assert.equal(visible.includes(hidden), false, `${hidden} must not be visible to customers`)
  }
})

test('admin keeps every workspace section; system admin adds the five global ones', () => {
  // 普通 admin / owner 看不到 5 个系统管理分区（requiresSystemAdmin）；演示用的管理员是系统管理员。
  // 26 个基础分区里 agentlines 只对客户可见（C6）；user-management 只认能力投影
  // （F14）：owner 的投影含 user.view 可见；普通 admin 与跨空间超管（投影为空，
  // 不再由 canAccessAllTenants 放行）不可见。因此 admin 19、owner 20、
  // crossTenant 19（F14 前为 20）、系统管理员 25。
  assert.equal(visibleFor('admin').length, 19)
  assert.equal(visibleFor('owner').length, 20)
  assert.equal(visibleFor('admin', { crossTenant: true }).length, 19)
  const systemAdmin = visibleFor('admin', { systemAdmin: true })
  assert.equal(systemAdmin.length, 25)
  assert.deepEqual(systemAdmin, baseKeys.filter((key) => key !== 'agentlines'))
})

test('user-management entry follows the user.view capability projection only (T09/AC-1, F14)', () => {
  // owner 投影含 user.view → 可见。
  assert.equal(visibleFor('owner').includes('user-management'), true)
  // 系统管理员与工作区角色无关：F1/F11 后能力投影为全量（含 user.view）→ 可见。
  assert.equal(visibleFor('owner', { systemAdmin: true }).includes('user-management'), true)
  assert.equal(visibleFor('admin', { systemAdmin: true }).includes('user-management'), true)
  assert.equal(visibleFor('contributor', { systemAdmin: true }).includes('user-management'), true)
  // 普通 admin / contributor / viewer 投影为空 → 不可见（后端仍会 403，C4）。
  assert.equal(visibleFor('admin').includes('user-management'), false)
  assert.equal(visibleFor('contributor').includes('user-management'), false)
  assert.equal(visibleFor('viewer').includes('user-management'), false)
  // F14：跨空间超管不再由 canAccessAllTenants 放行；当前空间角色投影无 user.view → 隐藏。
  assert.equal(visibleFor('admin', { crossTenant: true }).includes('user-management'), false)
  // 能力未加载 / 投影不含 user.view 时 fail-closed，即使身份是 owner 或系统管理员。
  assert.equal(visibleFor('owner', { caps: [] }).includes('user-management'), false)
  assert.equal(visibleFor('admin', { systemAdmin: true, caps: [] }).includes('user-management'), false)
  // 单一事实源：只要投影给出 user.view，入口就出现，不再叠加身份判断。
  assert.equal(visibleFor('admin', { caps: ['user.view'] }).includes('user-management'), true)
})

test('Settings.vue renders CustomerGeneralSettings only for customers, GeneralSettings otherwise', () => {
  assert.match(settingsSource, /<CustomerGeneralSettings v-if="isCustomerViewerActive" \/>/)
  assert.match(settingsSource, /<GeneralSettings v-else \/>/)
  assert.match(settingsSource, /const isCustomerViewerActive = computed\(\(\) => isCustomerViewer\(authStore\)\)/)
})

test('customer general settings only has language + theme (6 languages, no font/auto-update)', () => {
  const customerGeneral = readFileSync(new URL('./CustomerGeneralSettings.vue', import.meta.url), 'utf8')
  const languageOptions = customerGeneral.match(/<t-option\s+value="/g) ?? []
  assert.equal(languageOptions.length, 6, 'customer language picker keeps all 6 locales')
  assert.match(customerGeneral, /<BrandThemeToggle/)
  const withoutComments = customerGeneral
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[ \t])\/\/[^\n]*/gm, '')
  assert.doesNotMatch(withoutComments, /自动检查更新|auto.?update/i)
  assert.doesNotMatch(withoutComments, /字体|fontFamily|useFont/)
  assert.match(withoutComments, /localStorage\.setItem\('locale'/)
})
