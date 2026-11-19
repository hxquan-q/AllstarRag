/**
 * CP3 修复轮 1 复验（修-2 前端行为）：OperationLog 的 FS2 / OQ4+FS4 / FS3 前端半。
 *
 * 挂载真实 OperationLog.vue 脚本（vueFixture），断言：
 * - FS2：accepted / partial / canceled 复用 knowledgeEditor.activity.outcomes 中文键，
 *   success/denied/failed 口径不变；
 * - OQ4/FS4：目标列 title（文档名）→ name（库名）→ target_user_id/「—」回退，
 *   非白名单动作不看 details；
 * - FS3 前端：19 项白名单以 `actions` 数组随请求下推；api 层序列化为 CSV。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { fixture, flush } from '../../test-utils/vueFixture.ts'
import {
  OPERATION_LOG_AUDIT_ACTIONS,
  OPERATION_LOG_SCOPED_AUDIT_ACTIONS,
} from '../../i18n/auditActionRegistry.ts'

const OPERATIONS = new URL('./OperationLog.vue', import.meta.url)
const AUDIT_LOG_API = new URL('../../api/tenant/audit-log.ts', import.meta.url)

const i18n = { useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: { value: 'zh-CN' } }) }

function mountLog(t: TestContext) {
  const calls: any[][] = []
  const f = fixture(t)
  const { vm } = f.component(OPERATIONS, {
    'vue-i18n': i18n,
    '@/api/tenant/audit-log': {
      listAuditLog: async (...args: any[]) => {
        calls.push(args)
        return { success: true, data: [], next_cursor: 0 }
      },
    },
    '@/api/user-management': {
      listUsers: async () => ({ data: { items: [] } }),
      resolveUserManagementError: (error: unknown) => String(error),
    },
    '@/i18n/auditActionLabel': { auditActionLabel: (_t: unknown, action: string) => action },
    '@/i18n/auditActionRegistry': {
      AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' },
      OPERATION_LOG_AUDIT_ACTIONS,
    },
  }, { tenantId: 7 })
  return { vm: vm as any, calls }
}

test('CP3-FS2 accepted/partial/canceled 复用中文 outcome 词条，success/denied 不变', async (t) => {
  const { vm } = mountLog(t)
  await flush()
  assert.equal(vm.outcomeLabel('accepted'), 'knowledgeEditor.activity.outcomes.accepted')
  assert.equal(vm.outcomeLabel('partial'), 'knowledgeEditor.activity.outcomes.partial')
  assert.equal(vm.outcomeLabel('canceled'), 'knowledgeEditor.activity.outcomes.canceled')
  assert.equal(vm.outcomeLabel('success'), 'userManagement.operations.outcomeSuccess')
  assert.equal(vm.outcomeLabel('denied'), 'userManagement.operations.outcomeFailed')
  assert.equal(vm.outcomeLabel('failed'), 'userManagement.operations.outcomeFailed')
})

test('CP3-OQ4/FS4 目标列 title→name→target_user_id→「—」回退，非白名单不看 details', async (t) => {
  const { vm } = mountLog(t)
  await flush()

  const title = vm.targetLabel({
    action: 'knowledge.created', target_user_id: 'u-1', details: { title: '季度报告.pdf', name: '知识库A' },
  })
  assert.equal(title, '季度报告.pdf', '文档类行优先显示 details.title')

  const name = vm.targetLabel({
    action: 'kb.created', target_user_id: '', details: { name: '知识库A' },
  })
  assert.equal(name, '知识库A', '知识库行显示 details.name')

  const user = vm.targetLabel({
    action: 'knowledge.deleted', target_user_id: 'u-2', details: {},
  })
  assert.equal(user, 'userManagement.operations.unknownUser', '无文档名/库名回退 target_user_id（未解析到用户名时显示未知用户键）')

  const empty = vm.targetLabel({
    action: 'knowledge.deleted', target_user_id: '', details: {},
  })
  assert.equal(empty, '—', '全空回退「—」')

  const noise = vm.targetLabel({
    action: 'rbac.access_denied', target_user_id: 'u-3', details: { title: '不该显示.pdf' },
  })
  assert.equal(noise, 'userManagement.operations.unknownUser', '非白名单动作的 details 不参与目标列')

  // details 为字符串（旧行 JSON 原样）时不得崩、按回退处理。
  const raw = vm.targetLabel({
    action: 'knowledge.created', target_user_id: 'u-4', details: '{"title":"x"}',
  })
  assert.equal(raw, 'userManagement.operations.unknownUser', '字符串 details 不渲染 title')
})

test('CP3-FS3 前端：19 项白名单以 actions 数组下推（含 8 个 KB 动作）', async (t) => {
  const { calls } = mountLog(t)
  await flush()

  assert.equal(calls.length, 1, '首屏一次审计请求')
  const params = calls[0]![1]
  assert.equal((params.actions as string[]).join(','), [...OPERATION_LOG_AUDIT_ACTIONS].join(','),
    'actions 与 registry 单一来源一致')
  assert.equal(params.actions.length, 19)
  for (const action of OPERATION_LOG_SCOPED_AUDIT_ACTIONS) {
    assert.ok((params.actions as string[]).includes(action), `${action} 必须下推`)
  }
  assert.equal((params.actions as string[]).includes('rbac.access_denied'), false, '噪音动作不得下推')
  assert.equal(params.include_scoped, true, 'include_scoped 行为不变')

  // 组件确实引用 registry 常量（不是自建第二份清单）。
  const source = readFileSync(OPERATIONS, 'utf8')
  assert.match(source, /actions:\s*\[\.\.\.MEMBER_ACTIONS\]/)
  assert.match(source, /const MEMBER_ACTIONS = OPERATION_LOG_AUDIT_ACTIONS/)
})

test('CP3-FS3 api 层：actions 数组序列化为逗号分隔 CSV', async (t) => {
  const f = fixture(t)
  const urls: string[] = []
  const api = f.evaluate(`import { URLSearchParams } from 'node:url'\n${readFileSync(AUDIT_LOG_API, 'utf8')}`, {
    'node:url': { URLSearchParams },
    '@/utils/request': {
      get: async (url: string) => {
        urls.push(url)
        return {}
      },
    },
  }) as { listAuditLog: (tenantId: number, params?: Record<string, unknown>) => Promise<unknown> }

  await api.listAuditLog(7, { actions: ['knowledge.created', 'kb.created'], include_scoped: true })
  assert.equal(
    urls[0],
    '/api/v1/tenants/7/audit-log?actions=knowledge.created%2Ckb.created&include_scoped=true',
    'actions 以 CSV 拼参（顺序保持）',
  )

  await api.listAuditLog(7, { actions: [] })
  assert.equal(urls[1], '/api/v1/tenants/7/audit-log', '空数组不拼参数（旧形态不变）')

  await api.listAuditLog(7, { action: 'kb.created', actions: ['knowledge.created', 'kb.created'] })
  assert.equal(
    urls[2],
    '/api/v1/tenants/7/audit-log?action=kb.created&actions=knowledge.created%2Ckb.created',
    '单值 action 与 actions 并存（后端叠加生效）',
  )
})
