/**
 * 操作确认弹窗（T08 / AC-16、AC-19、AC-23、AC-13、AC-29、AC-31）。
 *
 * 验证封禁/解封/彻底删除/交接四个弹窗的提示、二次确认与请求参数；
 * 彻底删除必须输入目标邮箱，交接必须选择新 owner。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { fixture } from '../../test-utils/vueFixture.ts'
import type { UserRow } from './types.ts'

const SFC = new URL('./UserActionDialogs.vue', import.meta.url)

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
  target: UserRow = TARGET,
  candidates: UserRow[] = [TARGET, OTHER],
) {
  const calls = {
    ban: [] as any[],
    unban: [] as any[],
    purge: [] as any[],
    transfer: [] as any[],
    errors: [] as string[],
    successes: [] as string[],
  }
  const f = fixture(t)
  const { vm } = f.component(SFC, {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
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
      resolveUserManagementError: (error: unknown) =>
        `resolved:${error instanceof Error ? error.message : String(error)}`,
    },
  }, { tenantId: 7, target, mode, candidates })
  return { vm: vm as any, calls }
}

test('封禁弹窗：标题/说明/危险按钮与请求参数（AC-15/16）', async (t) => {
  const { vm, calls } = mountDialog(t, 'ban')
  assert.equal(vm.dialogTitle, 'userManagement.dialogs.banTitle')
  assert.equal(vm.dialogBody, 'userManagement.dialogs.banBody')
  assert.equal(vm.confirmButton.theme, 'danger')
  assert.equal(vm.targetLabel, 'target · target@example.com')

  vm.form.reason = 'spam'
  const ok = await vm.submitBanOrUnban()
  assert.equal(ok, true)
  assert.deepEqual(calls.ban, [[7, 'u-target', 'spam']])
  assert.deepEqual(calls.successes, ['userManagement.toasts.banSuccess'])
})

test('解封弹窗使用主色按钮并调用解封接口（AC-17）', async (t) => {
  const { vm, calls } = mountDialog(t, 'unban', { ...TARGET, status: 'banned' })
  assert.equal(vm.dialogTitle, 'userManagement.dialogs.unbanTitle')
  assert.equal(vm.confirmButton.theme, 'primary')
  const ok = await vm.submitBanOrUnban()
  assert.equal(ok, true)
  assert.deepEqual(calls.unban, [[7, 'u-target', undefined]])
})

test('彻底删除：邮箱不一致时拒绝且不调用接口（AC-23）', async (t) => {
  const { vm, calls } = mountDialog(t, 'purge')
  assert.equal(vm.dialogTitle, 'userManagement.dialogs.purgeTitle')
  assert.equal(vm.confirmButton.theme, 'danger')

  vm.form.confirmEmail = 'wrong@example.com'
  const ok = await vm.submitPurge()
  assert.equal(ok, false)
  assert.equal(calls.purge.length, 0)
  assert.deepEqual(calls.errors, ['userManagement.dialogs.purgeEmailMismatch'])
})

test('彻底删除：邮箱一致时带 confirm_email 调用接口（AC-23/35）', async (t) => {
  const { vm, calls } = mountDialog(t, 'purge')
  vm.form.confirmEmail = '  target@example.com  '
  vm.form.reason = 'inactive account'
  const ok = await vm.submitPurge()
  assert.equal(ok, true)
  assert.equal(calls.purge.length, 1)
  const [tenantId, userId, payload] = calls.purge[0]!
  assert.equal(tenantId, 7)
  assert.equal(userId, 'u-target')
  assert.equal(payload.confirm_email, 'target@example.com')
  assert.equal(payload.reason, 'inactive account')
  assert.deepEqual(calls.successes, ['userManagement.toasts.purgeSuccess'])
})

test('交接弹窗：候选人排除被交接人，必须选择新 owner（AC-13）', async (t) => {
  const { vm, calls } = mountDialog(t, 'transfer', { ...TARGET, role: 'owner' })
  assert.equal(vm.dialogTitle, 'userManagement.dialogs.transferTitle')
  assert.deepEqual([...vm.transferOptions].map((option: any) => option.value), ['u-other'])

  const missing = await vm.submitTransfer()
  assert.equal(missing, false)
  assert.equal(calls.transfer.length, 0)
  assert.deepEqual(calls.errors, ['userManagement.dialogs.transferTargetRequired'])

  vm.transferForm.toUserId = 'u-other'
  const ok = await vm.submitTransfer()
  assert.equal(ok, true)
  assert.equal(calls.transfer.length, 1)
  const [tenantArg, payload] = calls.transfer[0]!
  assert.equal(tenantArg, 7)
  assert.equal(payload.from_user_id, 'u-target')
  assert.equal(payload.to_user_id, 'u-other')
})
