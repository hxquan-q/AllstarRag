/**
 * T01 票级验证（member-join-label AC-4）：member_added 行两分支动作文案。
 *
 * - a. actor_user_id === target_user_id（自助加入：接受邀请/注册即入）→ 新键
 *   「加入空间」（真实 zh-CN bundle 逐字）；
 * - b. 两者不等（管理员手动添加）→ 维持既有「新增成员」；
 * - c. 对照：其他动作（member_removed / knowledge.created / owner_transferred）
 *   不进特判，显示名不变；
 * - d. 六语言 memberSelfJoined 键齐备非空，且与既有 member_added 显示名区分。
 *
 * 挂载方式沿 t05OperationLogScoped 先例（t46SfcMount 完整编译模板 + 真实
 * auditActionLabel/registry 依赖）。结构 renderer 下 t-table 的作用域插槽
 * 不执行，故模板分支用「源码结构锁 + 执行同一行内绑定（isSelfJoin /
 * actionLabel / t）」双重断言（T05-D 同款口径）。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { auditActionLabel } from '../../i18n/auditActionLabel.ts'
import { AUDIT_ACTION_I18N_ROOTS, OPERATION_LOG_AUDIT_ACTIONS } from '../../i18n/auditActionRegistry.ts'
import zhCN from '../../i18n/locales/zh-CN.ts'
import { getLocaleValueAtPath, LOCALE_BUNDLES, type LocaleName } from '../../i18n/localeKeyAudit.ts'
import { mountRendered, settle } from './t46SfcMount.ts'

const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)

const SELF_JOIN_KEY = 'userManagement.operations.memberSelfJoined'
const MEMBER_ADDED_LABEL_PATH = 'tenantMember.audit.action.rbac.member_added'

/** 逐段解析 zh-CN bundle 的路径（t05 同款）。 */
function resolveLocalePath(source: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[part]
    return undefined
  }, source)
}

/** t() 打桩走真实 zh-CN bundle（cp3Fix2 final-L1 先例）：断言真实词条而非复制的第二份文案。 */
function zhTranslate(key: string): string {
  const value = getLocaleValueAtPath(LOCALE_BUNDLES['zh-CN'], key)
  return typeof value === 'string' ? value : key
}

/** 真实 zh-CN 文案的 i18n 打桩 + 真实 auditActionLabel / registry（t05 同款）。 */
const realDeps = {
  'vue-i18n': {
    useI18n: () => ({
      t: zhTranslate,
      tm: (root: string) => resolveLocalePath(zhCN, root) ?? {},
      locale: { value: 'zh-CN' },
    }),
  },
  '@/i18n/auditActionLabel': { auditActionLabel },
  '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS, OPERATION_LOG_AUDIT_ACTIONS },
}

const row = (id: number, action: string, actor: string, target: string) => ({
  id,
  action,
  actor_user_id: actor,
  target_user_id: target,
  outcome: 'success',
  created_at: '2026-11-20T00:00:00Z',
})

/** 挂载完整 OperationLog（首屏一次审计请求返回 rows）。 */
function mountLog(t: TestContext, rows: Array<Record<string, unknown>>) {
  return mountRendered(t, {
    component: OPERATIONS,
    dependencies: {
      ...realDeps,
      '@/api/tenant/audit-log': {
        listAuditLog: async () => ({ success: true, data: rows, next_cursor: 0 }),
      },
      '@/api/user-management': {
        listUsers: async () => ({ data: { items: [] } }),
        resolveUserManagementError: (error: unknown) => String(error),
      },
    },
    props: { tenantId: 7 },
  })
}

test('T01-A 自助加入行（member_added 且 actor==target）动作列显示「加入空间」', async (t) => {
  const selfJoin = row(1, 'rbac.member_added', 'u-t05', 'u-t05')
  const { vm } = mountLog(t, [selfJoin])
  await settle()

  assert.equal(vm.isSelfJoin(selfJoin), true, '本人加入行进入新键分支（AC-1）')

  // v-if 分支渲染的 t() 键走真实 zh-CN bundle，逐字锁文案（AC-1/AC-2）。
  assert.equal(zhTranslate(SELF_JOIN_KEY), '加入空间', '真实 zh-CN 词条 = 「加入空间」')

  // 源码结构锁（T05-D 同款口径）：特判分支存在且渲染新键。
  const source = readFileSync(OPERATIONS, 'utf8')
  assert.match(source, /v-if="isSelfJoin\(row\)"/, '动作单元格存在 isSelfJoin 特判分支')
  const keyPattern = SELF_JOIN_KEY.replace(/\./g, '\\.')
  assert.match(
    source,
    new RegExp(`v-if="isSelfJoin\\(row\\)"[\\s\\S]{0,200}?t\\('${keyPattern}'\\)`),
    '特判分支渲染 memberSelfJoined 新键',
  )
})

test('T01-B 管理员手动添加行（actor!=target）维持「新增成员」', async (t) => {
  const manual = row(2, 'rbac.member_added', 'u-owner', 'u-new')
  const { vm } = mountLog(t, [manual])
  await settle()

  assert.equal(vm.isSelfJoin(manual), false, '手动添加行不进特判（AC-1）')
  // v-else 分支绑定 actionLabel：经真实 auditActionLabel + 真实 zh-CN bag 解析。
  assert.equal(vm.actionLabel('rbac.member_added'), '新增成员', '手动添加显示名逐字不变（AC-1）')
})

test('T01-C 其他动作渲染不受 isSelfJoin 影响（AC-3 对照）', async (t) => {
  const rows = [
    row(3, 'rbac.member_removed', 'u-x', 'u-x'), // actor==target 但非 member_added
    row(4, 'knowledge.created', 'u-x', 'u-x'), // KB 动作同样不进特判
    row(5, 'rbac.owner_transferred', 'u-owner', 'u-new'),
  ]
  const { vm } = mountLog(t, rows)
  await settle()

  for (const r of rows) {
    assert.equal(vm.isSelfJoin(r), false, `${r.action} 不进特判（即使 actor==target）`)
  }
  assert.equal(vm.actionLabel('rbac.member_removed'), '移除成员', 'member_removed 显示名不变')
  assert.equal(vm.actionLabel('knowledge.created'), '上传文档', 'knowledge.created 显示名不变')
  assert.equal(vm.actionLabel('rbac.owner_transferred'), '交接空间所有权', 'owner_transferred 显示名不变')

  // 既有结构锁（t05:240 同款）仍成立：普通动作单元格绑定 actionLabel 不变。
  assert.match(
    readFileSync(OPERATIONS, 'utf8'),
    /\{\{\s*actionLabel\(row\.action\)\s*\}\}/,
    'v-else 分支保持 actionLabel 原字面绑定（AC-3）',
  )
})

test('T01-D 六语言 memberSelfJoined 键齐备非空，既有 member_added 显示名键不受影响', () => {
  for (const name of Object.keys(LOCALE_BUNDLES) as LocaleName[]) {
    const fresh = getLocaleValueAtPath(LOCALE_BUNDLES[name], SELF_JOIN_KEY)
    assert.equal(typeof fresh, 'string', `${name} memberSelfJoined 应为字符串`)
    assert.ok((fresh as string).trim().length > 0, `${name} memberSelfJoined 非空`)

    const old = getLocaleValueAtPath(LOCALE_BUNDLES[name], MEMBER_ADDED_LABEL_PATH)
    assert.ok(typeof old === 'string' && old.trim().length > 0, `${name} 既有 member_added 显示名仍在（AC-2）`)
    assert.notEqual(fresh, old, `${name} 新键与旧显示名不同（区分生效）`)
  }
  assert.equal(getLocaleValueAtPath(LOCALE_BUNDLES['zh-CN'], SELF_JOIN_KEY), '加入空间', 'zh-CN 新键逐字')
  assert.equal(getLocaleValueAtPath(LOCALE_BUNDLES['zh-CN'], MEMBER_ADDED_LABEL_PATH), '新增成员', 'zh-CN 既有键逐字')
})
