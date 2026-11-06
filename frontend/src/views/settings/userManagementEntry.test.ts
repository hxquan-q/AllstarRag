/**
 * Settings / UserMenu 的用户管理入口（T09 / AC-1、AC-2）。
 *
 * - Settings 新增 `user-management` 分区并挂载页面；
 * - UserMenu「成员管理」指向新页面，只对 owner / 超管 / 跨空间超管展示；
 * - 普通 admin / contributor / viewer 不显示入口。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed } from 'vue'
import { SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE } from '../../config/settingsAccess.ts'

const settingsSource = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')
const userMenuSource = readFileSync(new URL('../../components/UserMenu.vue', import.meta.url), 'utf8')

test('Settings 的 navItems 与空间分组包含 user-management，并渲染 UserManagement（T09）', () => {
  assert.match(settingsSource, /\{ key: 'user-management', icon: 'usergroup', label: t\('userManagement\.title'\) \}/)
  // T20/Q31：旧「成员管理」入口隐藏，空间分组不再含 members。
  assert.match(settingsSource, /pickItems\(\['tenant', 'user-management', 'defaults'/)
  assert.doesNotMatch(settingsSource, /pickItems\(\[[^\]]*'members'/)
  assert.match(settingsSource, /currentSection === 'user-management'[\s\S]*?<UserManagement \/>/)
})

test('UserMenu「成员管理」指向 user-management 分区（T09）', () => {
  assert.match(userMenuSource, /@click="handleQuickNav\('user-management'\)"/)
  assert.equal(SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE['user-management'], 'owner')
})

function loadCanManageMembers(): (auth: Record<string, unknown>) => boolean {
  const script = userMenuSource.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)![1]!
  const ast = ts.createSourceFile('UserMenu.ts', script, ts.ScriptTarget.Latest, true)
  const statement = ast.statements.find(
    (candidate) =>
      ts.isVariableStatement(candidate) &&
      candidate.declarationList.declarations.some(
        (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'canManageMembers',
      ),
  )
  assert.ok(statement, 'UserMenu must define canManageMembers')
  const compiled = ts.transpileModule(statement.getText(ast), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
  return (auth) =>
    runInNewContext(`${compiled}\ncanManageMembers.value`, {
      computed,
      authStore: auth,
      SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE,
    }) as boolean
}

function auth(role: string, options: { systemAdmin?: boolean; crossTenant?: boolean } = {}) {
  const levels: Record<string, number> = { viewer: 1, contributor: 2, admin: 3, owner: 4 }
  return {
    role,
    isSystemAdmin: options.systemAdmin ?? false,
    canAccessAllTenants: options.crossTenant ?? false,
    hasRole: (min: string) => (levels[role] ?? 0) >= (levels[min] ?? 0),
  }
}

test('UserMenu 成员管理入口只对 owner / 超管 / 跨空间超管可见（AC-1）', () => {
  const canManageMembers = loadCanManageMembers()
  assert.equal(canManageMembers(auth('owner')), true)
  assert.equal(canManageMembers(auth('admin')), false)
  assert.equal(canManageMembers(auth('contributor')), false)
  assert.equal(canManageMembers(auth('viewer')), false)
  assert.equal(canManageMembers(auth('viewer', { systemAdmin: true })), true)
  assert.equal(canManageMembers(auth('viewer', { crossTenant: true })), true)
})

test('UserMenu 成员管理入口绑定在 canManageMembers 且客户视角隐藏（T09/C6）', () => {
  assert.match(
    userMenuSource,
    /v-if="canManageMembers && !isCustomer"[^>]*@click="handleQuickNav\('user-management'\)"/,
  )
})
