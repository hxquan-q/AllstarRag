/**
 * 用户管理的前端动作门控（T08 / AC-4、AC-5、AC-6、AC-7、AC-8、AC-12、AC-13、AC-29）。
 *
 * 这些断言只验证“界面门控”（C4）：owner 不能操作 owner、自己、超管；
 * 超管处理 owner 注销/降级前必须交接；角色下拉不含 owner。
 * 真正的权限仍由后端逐接口校验，后端用例已覆盖 403。
 */
import assert from 'node:assert/strict'
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

test('owner 不能封禁/解封/注销/降级任何其他 owner（AC-4/6/12）', () => {
  const ownerTarget = row({ user_id: 'u-other-owner', role: 'owner' })
  for (const kind of ['ban', 'unban', 'purge', 'changeRole'] as const) {
    const gate = gateUserAction(kind, ownerTarget, OWNER_ID, FULL_PERMISSIONS, false, 2)
    assert.equal(gate.allowed, false, `${kind} on another owner must be disabled for owner`)
    assert.equal(gate.reasonKey, 'userManagement.tooltips.ownerProtected')
  }
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

test('超管可以封禁/解封 owner，但注销/降级必须先交接（AC-8/13/20）', () => {
  const ownerTarget = row({ user_id: 'u-other-owner', role: 'owner' })
  const ownerCountTwo = gateUserAction('ban', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, 2)
  assert.equal(ownerCountTwo.allowed, true, 'system admin may ban an owner')

  const purge = gateUserAction('purge', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, 2)
  assert.equal(purge.allowed, false)
  assert.equal(purge.reasonKey, 'userManagement.tooltips.transferRequired')

  const demote = gateUserAction('changeRole', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, 2)
  assert.equal(demote.allowed, true, 'system admin may demote when another owner remains')

  const lastOwner = gateUserAction('changeRole', ownerTarget, ADMIN_ID, FULL_PERMISSIONS, true, 1)
  assert.equal(lastOwner.allowed, false)
  assert.equal(lastOwner.reasonKey, 'userManagement.tooltips.transferRequired')
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

test('角色下拉：owner 只看到 viewer/contributor/admin，超管含 owner（AC-12/28）', () => {
  assert.deepEqual(roleChangeOptions(false), ['viewer', 'contributor', 'admin'])
  assert.deepEqual(roleChangeOptions(true), ['viewer', 'contributor', 'admin', 'owner'])
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
