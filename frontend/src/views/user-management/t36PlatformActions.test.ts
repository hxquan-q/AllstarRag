/**
 * T36 / AC-2、AC-3：多空间操作分派与平台错误文案。
 *
 * - UserActionDialogs 在 platform 模式下 ban/unban/purge 调 T35 平台接口且不带 tenantId，
 *   transfer 直接拒绝不发请求；scope=current 仍走空间内接口（行为不变）。
 * - 通过 evaluate 执行真实 src/api/user-management.ts，验证平台错误状态码映射。
 * - usesPlatformAccountActions / canManageMembershipInScope 三值矩阵。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { fixture } from '../../test-utils/vueFixture.ts'
import { canManageMembershipInScope, usesPlatformAccountActions, type UserRow } from './types.ts'

const DIALOGS = new URL('./UserActionDialogs.vue', import.meta.url)
const API_SOURCE = readFileSync(new URL('../../api/user-management.ts', import.meta.url), 'utf8')

const i18n = { useI18n: () => ({ t: (key: string) => key }) }

const TARGET: UserRow = {
  user_id: 'u-target',
  email: 'target@example.com',
  username: 'target',
  role: 'viewer',
  status: 'active',
  created_at: '2026-11-05T10:00:00Z',
}

const OTHER: UserRow = {
  ...TARGET,
  user_id: 'u-other',
  email: 'other@example.com',
  username: 'other',
  role: 'admin',
}

function mountDialog(
  t: TestContext,
  mode: 'ban' | 'unban' | 'purge' | 'transfer',
  usePlatformApi: boolean,
  platformBanError?: unknown,
) {
  const calls = {
    ban: [] as any[],
    unban: [] as any[],
    purge: [] as any[],
    transfer: [] as any[],
    platformBan: [] as any[],
    platformUnban: [] as any[],
    platformPurge: [] as any[],
    errors: [] as string[],
    successes: [] as string[],
  }
  const f = fixture(t)
  const { vm } = f.component(DIALOGS, {
    'vue-i18n': i18n,
    'tdesign-vue-next': {
      MessagePlugin: {
        success: (key: string) => calls.successes.push(key),
        error: (key: string) => calls.errors.push(key),
        warning() {},
        info() {},
      },
    },
    '@/api/user-management': {
      banMember: async (...args: any[]) => { calls.ban.push(args); return { success: true } },
      unbanMember: async (...args: any[]) => { calls.unban.push(args); return { success: true } },
      purgeMember: async (...args: any[]) => { calls.purge.push(args); return { success: true } },
      transferOwner: async (...args: any[]) => { calls.transfer.push(args); return { success: true } },
      platformBanUser: async (...args: any[]) => {
        calls.platformBan.push(args)
        if (platformBanError !== undefined) throw platformBanError
        return { success: true }
      },
      platformUnbanUser: async (...args: any[]) => { calls.platformUnban.push(args); return { success: true } },
      platformPurgeUser: async (...args: any[]) => { calls.platformPurge.push(args); return { success: true } },
      resolveUserManagementError: (error: unknown) => `tenant:${(error as any)?.status ?? 'unknown'}`,
      resolvePlatformUserManagementError: (error: unknown) => `platform:${(error as any)?.status ?? 'unknown'}`,
    },
  }, {
    tenantId: 7,
    target: TARGET,
    mode,
    candidates: [TARGET, OTHER],
    usePlatformApi,
  })
  return { vm: vm as any, calls }
}

test('T36 AC-2 platform 模式封禁调平台接口且不带 tenantId', async (t) => {
  const { vm, calls } = mountDialog(t, 'ban', true)
  vm.form.reason = '  spam  '
  const ok = await vm.submitBanOrUnban()
  assert.equal(ok, true)
  assert.deepEqual(calls.platformBan, [['u-target', 'spam']])
  assert.equal(calls.ban.length, 0, '不得再调空间内封禁接口')
  assert.deepEqual(calls.successes, ['userManagement.toasts.banSuccess'])
})

test('T36 AC-2 platform 模式解封调平台接口且不带 tenantId', async (t) => {
  const { vm, calls } = mountDialog(t, 'unban', true)
  const ok = await vm.submitBanOrUnban()
  assert.equal(ok, true)
  assert.deepEqual(calls.platformUnban, [['u-target', undefined]])
  assert.equal(calls.unban.length, 0)
})

test('T36 AC-2 platform 模式注销调平台接口且不带 tenantId', async (t) => {
  const { vm, calls } = mountDialog(t, 'purge', true)
  vm.form.confirmEmail = '  target@example.com  '
  vm.form.reason = '  clean  '
  const ok = await vm.submitPurge()
  assert.equal(ok, true)
  assert.equal(calls.platformPurge.length, 1)
  const [userId, payload] = calls.platformPurge[0]!
  assert.equal(userId, 'u-target')
  assert.equal(payload.confirm_email, 'target@example.com')
  assert.equal(payload.reason, 'clean')
  assert.equal(calls.purge.length, 0, '不得再调空间内注销接口')
})

test('T36 AC-2 platform 模式交接直接拒绝且不发任何请求', async (t) => {
  const { vm, calls } = mountDialog(t, 'transfer', true)
  vm.transferForm.toUserId = 'u-other'
  const ok = await vm.submitTransfer()
  assert.equal(ok, false)
  assert.equal(calls.transfer.length, 0)
  assert.equal(calls.platformBan.length + calls.platformUnban.length + calls.platformPurge.length, 0)

  // 走完整 submit 入口同样不发请求、不报成功。
  await vm.submit()
  assert.equal(calls.transfer.length, 0)
  assert.deepEqual(calls.successes, [])
})

test('T36 AC-3 平台接口失败用平台文案（403）', async (t) => {
  const { vm, calls } = mountDialog(t, 'ban', true, { status: 403 })
  await vm.submit()
  assert.deepEqual(calls.errors, ['platform:403'])
  assert.equal(calls.successes.length, 0)
})

test('T36 回归 scope=current 仍走空间内接口（行为不变）', async (t) => {
  const { vm, calls } = mountDialog(t, 'ban', false)
  vm.form.reason = 'spam'
  const ok = await vm.submitBanOrUnban()
  assert.equal(ok, true)
  assert.deepEqual(calls.ban, [[7, 'u-target', 'spam']])
  assert.equal(calls.platformBan.length, 0)
})

test('T36 AC-3 resolvePlatformUserManagementError 状态码映射与 unknown 回退', (t) => {
  const f = fixture(t)
  const keys: string[] = []
  const api = f.evaluate(API_SOURCE, {
    '@/utils/request': { get: async () => ({}), post: async () => ({}) },
    '@/i18n': {
      __esModule: true,
      default: { global: { t: (key: string) => { keys.push(key); return `T:${key}` } } },
    },
  }) as any

  assert.equal(api.resolvePlatformUserManagementError({ status: 403 }), 'T:userManagement.errors.platformForbidden')
  assert.equal(api.resolvePlatformUserManagementError({ status: 404 }), 'T:userManagement.errors.platformNotFound')
  assert.equal(api.resolvePlatformUserManagementError({ status: 409 }), 'T:userManagement.errors.platformConflict')
  assert.equal(api.resolvePlatformUserManagementError({ status: 400 }), 'T:userManagement.errors.invalidRequest')
  assert.equal(api.resolvePlatformUserManagementError({ status: 500 }), 'T:userManagement.errors.unknown')
  assert.equal(api.resolvePlatformUserManagementError(null), 'T:userManagement.errors.unknown')
  assert.equal(api.resolvePlatformUserManagementError({}), 'T:userManagement.errors.unknown')

  // 对照：空间内接口 404 仍是 notFound，不与平台语义混用。
  keys.length = 0
  assert.equal(api.resolveUserManagementError({ status: 404 }), 'T:userManagement.errors.notFound')
  assert.deepEqual(keys, ['userManagement.errors.notFound'])
})

test('T36 AC-2 usesPlatformAccountActions / canManageMembershipInScope 三值矩阵', () => {
  assert.equal(usesPlatformAccountActions('current'), false)
  assert.equal(usesPlatformAccountActions('other'), true)
  assert.equal(usesPlatformAccountActions('all'), true)

  assert.equal(canManageMembershipInScope('current'), true)
  assert.equal(canManageMembershipInScope('other'), false)
  assert.equal(canManageMembershipInScope('all'), false)
})
