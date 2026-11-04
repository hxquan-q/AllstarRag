import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { parse } from '@vue/compiler-sfc'
import { createSSRApp, reactive, ref } from 'vue'
import { renderToString } from '@vue/server-renderer'
import zhCN from '../../i18n/locales/zh-CN'
import { fixture, flush } from '../../test-utils/vueFixture.ts'

// T04 整体回归：owner 共享链接弹层的人数/有效期校验与列表展示。
// 表单行为用 vueFixture 驱动真实 setup；列表“已注册 X/Y”“已加入 N”用
// SSR 渲染组件源码中的真实模板片段，配上真实 zh-CN 文案。

function translate(message: any, key: string, params: Record<string, unknown> = {}): string {
  const value = key.split('.').reduce<any>((node, part) => node?.[part], message)
  if (typeof value !== 'string') throw new Error(`缺少 i18n key: ${key}`)
  return value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
}

type CreateCall = { tenantId: number; body: any }

async function shareLinkFixture(t: TestContext) {
  const f = fixture(t)
  const errors: string[] = []
  const createCalls: CreateCall[] = []
  const auth = reactive({
    currentTenantId: 7,
    currentTenantRole: 'owner',
    canAccessAllTenants: false,
    user: { id: 'current-user' },
  })
  const emptyList = { success: true, data: { invitations: [], total: 0 } }
  const { vm, close } = f.component(new URL('./TenantMembers.vue', import.meta.url), {
    'vue-i18n': {
      useI18n: () => ({ t: (key: string) => key, tm: () => ({}), locale: ref('zh-CN') }),
    },
    'tdesign-vue-next': {
      MessagePlugin: {
        success() {},
        info() {},
        error: (message: string) => errors.push(message),
      },
    },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/clipboard': { copyWithToast: async () => {} },
    '@/utils/docsUrl': { docsUrl: () => '' },
    '@/utils/memberRoles': {
      allowedCreateRoles: () => [],
      canCreateAccount: () => false,
      canResetTarget: () => false,
    },
    '@/i18n/auditActionRegistry': { AUDIT_ACTION_I18N_ROOTS: { tenantMember: '' } },
    '@/i18n/auditActionLabel': { auditActionLabel: () => '' },
    '@/api/tenant/members': {
      listMembers: async () => ({
        success: true,
        data: { members: [], total: 0, page: 1, page_size: 20 },
      }),
      updateMemberRole: async () => ({ success: true }),
      removeMember: async () => ({ success: true }),
    },
    '@/api/tenant/invitations': {
      listTenantInvitations: async () => emptyList,
      createInvitation: async () => ({ success: true }),
      revokeInvitation: async () => ({ success: true }),
      createInviteLink: async (tenantId: number, body: any) => {
        createCalls.push({ tenantId, body })
        return {
          success: true,
          data: {
            id: 1,
            invite_url: '/register?token=t',
            max_registrations: body.max_registrations ?? null,
            registered_count: 0,
          },
        }
      },
    },
    '@/api/tenant/audit-log': {},
  })
  await flush()
  return { vm, close, auth, errors, createCalls, f }
}

for (const value of [0, 1001, 1.5]) {
  test(`人数非法（${value}）阻止提交并提示`, async (t: TestContext) => {
    const f = await shareLinkFixture(t)
    f.vm.shareLinkForm.maxRegistrations = value
    await f.vm.submitShareLink()
    assert.deepEqual(f.errors, ['tenantInvitation.shareLink.invalidMaxRegistrations'])
    assert.equal(f.createCalls.length, 0, '不应调用创建接口')
  })
}

test('人数留空表示不限：请求不带 max_registrations', async (t: TestContext) => {
  const f = await shareLinkFixture(t)
  f.vm.shareLinkForm.maxRegistrations = null
  await f.vm.submitShareLink()
  assert.equal(f.createCalls.length, 1)
  assert.equal(f.createCalls[0].tenantId, 7)
  assert.equal('max_registrations' in f.createCalls[0].body, false)
})

for (const value of [1, 1000]) {
  test(`人数 ${value} 是合法边界，按填写值提交`, async (t: TestContext) => {
    const f = await shareLinkFixture(t)
    f.vm.shareLinkForm.maxRegistrations = value
    await f.vm.submitShareLink()
    assert.equal(f.createCalls.length, 1)
    assert.equal(f.createCalls[0].body.max_registrations, value)
  })
}

for (const days of [7, 30, 90]) {
  test(`有效期预设 ${days} 天按天累加`, async (t: TestContext) => {
    const f = await shareLinkFixture(t)
    f.vm.shareLinkForm.expiryPreset = String(days) as '7' | '30' | '90'
    const before = Date.now()
    await f.vm.submitShareLink()
    const expiresAt = new Date(f.createCalls[0].body.expires_at).getTime()
    const delta = expiresAt - before
    assert.ok(delta > (days * 24 * 60 - 2) * 60 * 1000, '过期时间过短')
    assert.ok(delta < (days * 24 * 60 + 2) * 60 * 1000, '过期时间过长')
  })
}

test('自定义有效期在过去时被拒绝', async (t: TestContext) => {
  const f = await shareLinkFixture(t)
  f.vm.shareLinkForm.expiryPreset = 'custom'
  f.vm.shareLinkForm.customExpiry = '2020-01-02 03:04'
  await f.vm.submitShareLink()
  assert.deepEqual(f.errors, ['tenantInvitation.shareLink.invalidExpiry'])
  assert.equal(f.createCalls.length, 0)
})

test('自定义有效期在未来时按输入提交', async (t: TestContext) => {
  const f = await shareLinkFixture(t)
  f.vm.shareLinkForm.expiryPreset = 'custom'
  f.vm.shareLinkForm.customExpiry = '2030-01-02 03:04'
  await f.vm.submitShareLink()
  assert.equal(f.createCalls.length, 1)
  const expected = new Date('2030-01-02T03:04').toISOString()
  assert.equal(f.createCalls[0].body.expires_at, expected)
})

test('弹层每次重新打开都重置人数/有效期/结果', async (t: TestContext) => {
  const f = await shareLinkFixture(t)
  f.vm.shareLinkPopupVisible = true
  await flush()
  f.vm.shareLinkForm.maxRegistrations = 9
  f.vm.shareLinkForm.expiryPreset = 'custom'
  f.vm.shareLinkForm.customExpiry = '2030-01-02 03:04'
  f.vm.shareLinkResult = { id: 1 }
  f.vm.shareLinkPopupVisible = false
  await flush()
  f.vm.shareLinkPopupVisible = true
  await flush()
  assert.equal(f.vm.shareLinkForm.maxRegistrations, null)
  assert.equal(f.vm.shareLinkForm.expiryPreset, '7')
  assert.equal(f.vm.shareLinkForm.customExpiry, '')
  assert.equal(f.vm.shareLinkResult, null)
})

// 取包含 marker 的 <t-form ...> 开始标签原文（到第一个 > 为止）。
function formOpenTag(marker: string): string {
  const { descriptor } = parse(readFileSync(new URL('./TenantMembers.vue', import.meta.url), 'utf8'))
  const template = descriptor.template!.content
  const markerIdx = template.indexOf(marker)
  assert.ok(markerIdx >= 0, `模板中找不到 ${marker}`)
  const start = template.lastIndexOf('<t-form', markerIdx)
  assert.ok(start >= 0, `找不到包含 ${marker} 的 t-form 开始标签`)
  const end = template.indexOf('>', markerIdx)
  return template.slice(start, end + 1)
}

test('共享链接表单标签列加宽到 104px，邮件邀请表单维持 80 不动（F15）', () => {
  const shareForm = formOpenTag(':data="shareLinkForm"')
  assert.ok(
    shareForm.includes(':label-width="104"'),
    `共享链接表单标签列应为 104（容纳「最多注册人数」六字标签）：${shareForm}`,
  )
  const inviteForm = formOpenTag('class="member-invite-form"')
  assert.ok(
    inviteForm.includes(':label-width="80"'),
    `邮件邀请表单应维持原有 80 不动：${inviteForm}`,
  )
})

function shareLinkCellTemplate(): string {
  const { descriptor } = parse(readFileSync(new URL('./TenantMembers.vue', import.meta.url), 'utf8'))
  const template = descriptor.template!.content
  const marker = '<template v-if="row.is_share_link">'
  const start = template.indexOf(marker)
  assert.ok(start >= 0, '找不到共享链接展示模板')
  const end = template.indexOf('</template>', start + marker.length)
  assert.ok(end > start, '找不到共享链接展示模板结束位置')
  return `<div>${template.slice(start + marker.length, end)}</div>`
}

async function renderShareLinkCell(row: Record<string, unknown>) {
  const app = createSSRApp({ template: shareLinkCellTemplate(), data: () => ({ row }) })
  ;(app.config.globalProperties as any).$t = (
    key: string,
    params?: Record<string, unknown>,
  ) => translate(zhCN, key, params)
  app.component('t-icon', { template: '<span />' })
  return renderToString(app)
}

test('列表有限额时显示「已注册 X/Y 人」，并保留「已加入 N 人」', async () => {
  const html = await renderShareLinkCell({
    is_share_link: true,
    accepted_count: 5,
    registered_count: 3,
    max_registrations: 10,
  })
  assert.ok(html.includes('通过链接邀请'))
  assert.ok(html.includes('已加入 5 人'))
  assert.ok(html.includes('已注册 3/10 人'))
})

test('列表不限人数时只显示「已注册 X 人」；无人加入显示空态', async () => {
  const html = await renderShareLinkCell({
    is_share_link: true,
    accepted_count: 0,
    registered_count: 2,
    max_registrations: null,
  })
  assert.ok(html.includes('已注册 2 人'))
  assert.ok(!html.includes('已注册 2/'))
  assert.ok(html.includes('尚无成员加入'))
})
