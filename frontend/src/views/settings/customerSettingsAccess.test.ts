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
 * - 管理员 24 个基础分区（含 agentlines）中不出现 agentlines，其余完全不受影响
 *   （另有集成分区，不在本用例）；
 * - deep-link 非法 section 由路由归一化到 general（customerAccess.test.ts 已覆盖）。
 *
 * 基数是本分支的实测值：原 23 + agentlines = 24。本分支未合入上游 defaults 分区；
 * 若将来合并 release/merge-to-main，先按届时实际值更新，不要写死 25。
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

function visibleFor(role: 'viewer' | 'admin' | 'owner', options: { systemAdmin?: boolean; crossTenant?: boolean } = {}) {
  const auth = {
    role,
    isSystemAdmin: options.systemAdmin ?? false,
    canAccessAllTenants: options.crossTenant ?? false,
    hasRole: (min: string) => {
      const levels: Record<string, number> = { viewer: 1, contributor: 2, admin: 3, owner: 4 }
      return (levels[role] ?? 0) >= (levels[min] ?? 0)
    },
  }
  // 重新执行以注入当前 authStore（canSeeSection 闭包引用 authStore）
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
    },
  ) as never as (key: string) => boolean
  return baseKeys.filter((key) => fn(key))
}

test('settings navigation has 24 non-integration sections (29 with integrations)', () => {
  assert.equal(baseKeys.length, 24, `base settings keys: ${baseKeys.join(', ')}`)
  assert.deepEqual(baseKeys.slice(0, 3), ['general', 'ollama', 'weknoracloud'])
  assert.ok(baseKeys.includes('userprofile'))
  assert.ok(baseKeys.includes('assistant'))
  assert.ok(baseKeys.includes('mymemory'))
  assert.ok(baseKeys.includes('envvars'))
  assert.ok(baseKeys.includes('agentlines'), 'T03 adds the agent lines section')
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
  // 24 个基础分区里 agentlines 只对客户可见（C6），因此管理员侧仍为 18 / 23。
  assert.equal(visibleFor('admin').length, 18)
  assert.equal(visibleFor('owner').length, 18)
  assert.equal(visibleFor('admin', { crossTenant: true }).length, 18)
  const systemAdmin = visibleFor('admin', { systemAdmin: true })
  assert.equal(systemAdmin.length, 23)
  assert.deepEqual(systemAdmin, baseKeys.filter((key) => key !== 'agentlines'))
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
