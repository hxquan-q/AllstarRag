/**
 * 用户管理的前端动作门控（T08 / AC-4、AC-5、AC-6、AC-7、AC-8、AC-12、AC-13、AC-29）。
 *
 * 这些断言只验证“界面门控”（C4）：owner 不能操作 owner、自己、超管；
 * 超管处理 owner 注销/降级前必须交接。
 * CP3 修复轮 1（F2）：owner 行的角色下拉一律禁用（reasonKey=transferRequired），
 * 不再按 ownerCount 放行超管降级——后端对 owner → 非 owner 一律 403，必须先交接。
 * 真正的权限仍由后端逐接口校验，后端用例已覆盖 403。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import {
  gateUserAction,
  roleChangeOptions,
  systemAdminToUserRow,
  toUserRow,
  type UserActionPermissions,
  type UserRow,
} from './types.ts'

const FULL_PERMISSIONS: UserActionPermissions = {
  changeRole: true,
  ban: true,
  unban: true,
  purge: true,
  transfer: true,
}

function row(overrides: Partial<UserRow> = {}): UserRow {
  return {
    user_id: 'u-target',
    email: 'target@example.com',
    username: 'target',
    role: 'viewer',
    status: 'active',
    created_at: '2026-11-05T10:00:00Z',
    ...overrides,
  }
}

const OWNER_ID = 'u-owner'
const ADMIN_ID = 'u-admin'

test('owner 不能封禁/解封/注销任何其他 owner；owner 行改角色一律提示先交接（AC-4/6/12，F2）', () => {
  const ownerTarget = row({ user_id: 'u-other-owner', role: 'owner' })
  for (const kind of ['ban', 'unban', 'purge'] as const) {
    const gate = gateUserAction(kind, ownerTarget, OWNER_ID, FULL_PERMISSIONS, false, 2)
    assert.equal(gate.allowed, false, `${kind} on another owner must be disabled for owner`)
    assert.equal(gate.reasonKey, 'userManagement.tooltips.ownerProtected')
  }
  // F2：owner 行 changeRole 一律禁用并提示先交接，不再显示 ownerProtected，
  // 与后端「owner → 非 owner 一律 403」对齐。
  const changeRole = gateUserAction('changeRole', ownerTarget, OWNER_ID, FULL_PERMISSIONS, false, 2)
  assert.equal(changeRole.allowed, false)
  assert.equal(changeRole.reasonKey, 'userManagement.tooltips.transferRequired')
})

test('owner 可以操作本空间 viewer/contributor/admin（AC-3/29）', () => {
  for (const role of ['viewer', 'contributor', 'admin'] as const) {
    for (const kind of ['ban', 'unban', 'purge', 'changeRole'] as const) {
      const gate = gateUserAction(kind, row({ role }), OWNER_ID, FULL_PERMISSIONS, false, 2)
      assert.equal(gate.allowed, true, `${kind} on ${role} must be enabled for owner`)
    }
  }
})

test('任何人都不能操作自己（AC-7）', () => {
  const self = row({ user_id: OWNER_ID, role: 'owner' })
  for (const kind of ['ban', 'unban', 'purge', 'changeRole'] as const) {
    const gate = gateUserAction(kind, self, OWNER_ID, FULL_PERMISSIONS, false, 2)
    assert.equal(gate.allowed, false)
    assert.equal(gate.reasonKey, 'userManagement.tooltips.self')
  }
})

test('owner 不能操作系统管理员账号（AC-5）', () => {
  const systemAdmin = row({ user_id: 'u-sa', role: 'system_admin' })
  for (const kind of ['ban', 'unban', 'purge', 'changeRole'] as const) {
    const gate = gateUserAction(kind, systemAdmin, OWNER_ID, FULL_PERMISSIONS, false, 2)
    assert.equal(gate.allowed, false)
    assert.equal(gate.reasonKey, 'userManagement.tooltips.systemAdminProtected')
  }
})

test('超管可以封禁/解封 owner，但注销/降级一律先交接，ownerCount 不再放行（AC-8/13/20，F2）', () => {
  const ownerTarget = row({ user_id: 'u-other-owner', role: 'owner' })
  const ownerCountTwo = gateUserAction('ban', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, 2)
  assert.equal(ownerCountTwo.allowed, true, 'system admin may ban an owner')

  const purge = gateUserAction('purge', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, 2)
  assert.equal(purge.allowed, false)
  assert.equal(purge.reasonKey, 'userManagement.tooltips.transferRequired')

  // F2：即使仍有另一位 owner（ownerCount=2），超管也不能直接降级 owner；
  // ownerCount 不再参与门控，任何取值都一律禁用并提示先交接。
  for (const ownerCount of [1, 2, 3]) {
    const demote = gateUserAction('changeRole', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, ownerCount)
    assert.equal(demote.allowed, false, `ownerCount=${ownerCount} must not re-enable owner demotion`)
    assert.equal(demote.reasonKey, 'userManagement.tooltips.transferRequired')
  }
})

test('owner 行不渲染角色下拉：表格只渲染只读角色文本与交接提示（F2）', () => {
  // 门控层面：无论操作者是谁，owner 行的 changeRole 必带禁用原因，
  // UserTable 的角色单元格据此走 tooltip + 只读文本分支，t-select 不出现。
  const ownerRow = row({ user_id: 'u-other-owner', role: 'owner' })
  for (const isSystemAdmin of [false, true]) {
    const gate = gateUserAction('changeRole', ownerRow, ADMIN_ID, FULL_PERMISSIONS, isSystemAdmin, 2)
    assert.equal(gate.allowed, false, 'owner row changeRole must stay disabled')
    assert.ok(gate.reasonKey, 'owner row must carry a disabled reason so the select stays hidden')
  }
  // 组件层面（源码断言，与 customerSettingsAccess.test.ts 同模式）：
  // 1) 角色单元格先看 reasonKey（tooltip + 只读文本），只有 canChangeRole 才渲染 t-select；
  const tableSource = readFileSync(new URL('./UserTable.vue', import.meta.url), 'utf8')
  assert.match(tableSource, /v-if="roleGate\(row\)\.reasonKey"[\s\S]*?user-table__role-text/)
  // T17：空角色行（全平台视图无成员关系账号）也不渲染下拉，条件追加 `&& row.role`。
  assert.match(tableSource, /<t-select[\s\S]*?v-else-if="canChangeRole\(row\)( && row\.role)?"/)
  // 2) canChangeRole 完全由门控 allowed 决定，且走的就是 changeRole 门控；
  assert.match(tableSource, /function canChangeRole\(row: UserRow\): boolean \{\s*return roleGate\(row\)\.allowed/)
  assert.match(tableSource, /function roleGate\(row: UserRow\): ActionGate \{[\s\S]*?return gate\('changeRole', row\)/)
})

test('超管不能操作自己；可以注销普通账号（AC-7/8）', () => {
  // 超管行先命中“系统管理员受保护”分支，同样禁用；非超管的自操作命中 self。
  const self = row({ user_id: ADMIN_ID, role: 'system_admin' })
  assert.equal(gateUserAction('purge', self, ADMIN_ID, FULL_PERMISSIONS, true, 2).reasonKey,
    'userManagement.tooltips.systemAdminProtected')
  assert.equal(
    gateUserAction('ban', row({ user_id: ADMIN_ID, role: 'owner' }), ADMIN_ID, FULL_PERMISSIONS, true, 2)
      .reasonKey,
    'userManagement.tooltips.self',
  )
  assert.equal(gateUserAction('purge', row({ role: 'viewer' }), ADMIN_ID, FULL_PERMISSIONS, true, 2).allowed,
    true)
})

test('能力缺失时动作一律禁用（C4）', () => {
  const noPermissions: UserActionPermissions = {
    changeRole: false, ban: false, unban: false, purge: false, transfer: false,
  }
  for (const kind of ['ban', 'unban', 'purge', 'changeRole', 'transfer'] as const) {
    const gate = gateUserAction(kind, row({ role: 'viewer' }), OWNER_ID, noPermissions, false, 2)
    assert.equal(gate.allowed, false)
    assert.equal(gate.reasonKey, 'userManagement.tooltips.noPermission')
  }
})

test('交接只对 owner 行可用；owner 只能交接自己（AC-13）', () => {
  assert.equal(
    gateUserAction('transfer', row({ role: 'viewer' }), OWNER_ID, FULL_PERMISSIONS, true, 2).allowed,
    false,
  )
  assert.equal(
    gateUserAction('transfer', row({ user_id: OWNER_ID, role: 'owner' }), OWNER_ID, FULL_PERMISSIONS, false, 2).allowed,
    true,
  )
  assert.equal(
    gateUserAction('transfer', row({ user_id: 'u-other', role: 'owner' }), OWNER_ID, FULL_PERMISSIONS, false, 2)
      .reasonKey,
    'userManagement.tooltips.ownerProtected',
  )
})

test('角色下拉选项：owner 操作者只看到 游客/编辑，超管含 owner，admin 不再出现（AC-12/28/Q37）', () => {
  // F2 后下拉只会出现在非 owner 行上；超管列表里的 owner 选项用于把
  // viewer/contributor/admin 提升为 co-owner，不再用于降级 owner。
  assert.deepEqual(roleChangeOptions(false), ['viewer', 'contributor'])
  assert.deepEqual(roleChangeOptions(true), ['viewer', 'contributor', 'owner'])
  assert.equal(roleChangeOptions(true).includes('admin'), false)
  assert.equal(roleChangeOptions(false).includes('owner'), false)
})

test('后端列表行映射只保留展示字段，并归一化状态（AC-32）', () => {
  const mapped = toUserRow({
    user_id: 'u1',
    email: 'a@example.com',
    username: 'a',
    role: 'admin',
    status: 'banned',
    is_active: false,
    created_at: '2026-11-05T10:00:00Z',
  })
  assert.deepEqual(mapped, {
    user_id: 'u1',
    email: 'a@example.com',
    username: 'a',
    role: 'admin',
    status: 'banned',
    created_at: '2026-11-05T10:00:00Z',
  })
  assert.equal('password_hash' in mapped, false)
  assert.equal(
    toUserRow({
      user_id: 'u2', email: 'b@example.com', username: 'b', role: 'viewer',
      status: 'something-new', is_active: true, created_at: '',
    }).status,
    'active',
  )
})

test('超管行的状态由 is_active 映射为正常/已封禁（AC-17）', () => {
  const base = {
    avatar: '',
    is_system_admin: true,
    created_at: '',
    updated_at: '',
  }
  const active = systemAdminToUserRow({
    ...base, id: 'sa1', email: 'sa@example.com', username: 'sa', is_active: true,
  })
  assert.equal(active.role, 'system_admin')
  assert.equal(active.status, 'active')
  const banned = systemAdminToUserRow({
    ...base, id: 'sa2', email: 'sa2@example.com', username: 'sa2', is_active: false,
  })
  assert.equal(banned.status, 'banned')
})
