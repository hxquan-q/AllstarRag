/**
 * Settings / UserMenu 的用户管理入口（T09 / AC-1、AC-2；T08 同步）。
 *
 * - Settings 新增 `user-management` 分区并挂载页面；
 * - UserMenu「成员管理」指向新页面，对 owner / 普通管理员(admin) / 超管展示
 *   （T08：admin 获得成员管理入口；入口本身只认 user.view 能力投影，F33）；
 * - contributor / viewer 不显示入口。
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

test('UserMenu「成员管理」指向 user-management 分区（T09/T08）', () => {
  assert.match(userMenuSource, /@click="handleQuickNav\('user-management'\)"/)
  // T08：普通管理员获得成员管理入口，快捷入口门槛 owner→admin（现状 owner，预期红）。
  assert.equal(SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE['user-management'], 'admin')
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
  return (auth) => {
    // F33：入口只认能力投影，不再读角色/跨空间标志。
    // T08 后端能力矩阵（T02/T06 已落地）：owner 与 admin 都拿到 user.view，
    // contributor/viewer 空集——用角色模拟这两档能力投影。
    const capabilitiesStore = {
      can: (capability: string) =>
        capability === 'user.view'
        && (auth.role === 'owner' || auth.role === 'admin' || auth.isSystemAdmin === true),
    }
    return runInNewContext(`${compiled}\ncanManageMembers.value`, {
      computed,
      authStore: auth,
      capabilitiesStore,
      SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE,
    }) as boolean
  }
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

test('UserMenu 成员管理入口只认 capabilities.can(user.view)（AC-1/F33/T08）', () => {
  const canManageMembers = loadCanManageMembers()
  assert.equal(canManageMembers(auth('owner')), true)
  // T08：admin（普通管理员）拿到 user.view 能力，入口可见（基线：入口实现
  // 已纯能力驱动，后端 T02/T06 已授予；本断言防回归）。
  assert.equal(canManageMembers(auth('admin')), true)
  assert.equal(canManageMembers(auth('contributor')), false)
  assert.equal(canManageMembers(auth('viewer')), false)
  assert.equal(canManageMembers(auth('viewer', { systemAdmin: true })), true)
  // 跨空间标志本身不再是第二套权限来源；能力投影没有 user.view 就不显示。
  assert.equal(canManageMembers(auth('viewer', { crossTenant: true })), false)
})

test('UserMenu 成员管理入口绑定在 canManageMembers 且客户视角隐藏（T09/C6）', () => {
  assert.match(
    userMenuSource,
    /v-if="canManageMembers && !isCustomer"[^>]*@click="handleQuickNav\('user-management'\)"/,
  )
})
