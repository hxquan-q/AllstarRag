/**
 * AC-7：云之家员工账号在「个人资料 → 修改密码」处的分支。
 *
 * 判定只认服务端写入的 `preferences.yunzhijia_employee === true`：
 * - 员工：隐藏改密编辑入口，显示「员工账号请从云之家进入…」；
 * - 普通账号（viewer / owner / 超管，均无该标记）：入口与文案维持原样；
 * - OIDC 账号：既有隐藏分支保持不变。
 *
 * 挂载真实 UserProfile.vue（t46SfcMount 完整编译模板）；模板用 $t，
 * 通过 globalProperties 注入返回 key 的 t()，断言渲染的是哪个文案键。
 */
import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'

import { findByTag, mountRendered, settle, textOf, type T46Node } from '../user-management/t46SfcMount.ts'

const PROFILE = new URL('./UserProfile.vue', import.meta.url)

const EDIT_CLASS = 'edit-btn'
const EMPLOYEE_DESC = 'userProfile.changePassword.yunzhijiaEmployeeDescription'
const OIDC_DESC = 'userProfile.changePassword.oidcOnlyDescription'
const NORMAL_DESC = 'userProfile.changePassword.description'
const EDIT_LABEL = 'userProfile.changePassword.label'

function baseUser(overrides: Record<string, unknown> = {}) {
  return {
    id: 'u-ac7-placeholder',
    username: '占位用户',
    email: 'user-placeholder@example.invalid',
    tenant_id: '10024',
    created_at: '2026-11-24T00:00:00Z',
    updated_at: '2026-11-24T00:00:00Z',
    ...overrides,
  }
}

function mountProfile(t: TestContext, user: Record<string, unknown>) {
  return mountRendered(t, {
    component: PROFILE,
    dependencies: {
      'vue-router': { useRouter: () => ({ push() {} }) },
      'tdesign-vue-next': { MessagePlugin: { error() {}, success() {} } },
      '@/api/auth': {
        getCurrentUser: async () => ({ success: true, data: { user } }),
        getAuthConfig: async () => ({ complex_password_enabled: false }),
        changePassword: async () => ({ success: true }),
        logout: async () => {},
      },
      '@/stores/auth': { useAuthStore: () => ({ logout() {} }) },
      'vue-i18n': {
        useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }),
      },
      '@/utils/passwordPolicy': { newPasswordRules: () => [] },
    },
    globalProperties: { $t: (key: string) => key },
  })
}

function editButtons(root: T46Node): T46Node[] {
  return findByTag(root, 't-button').filter(
    (node) => node.props.class === EDIT_CLASS || node.props.title === EDIT_LABEL,
  )
}

test('AC-7 employee account hides the password editor and shows the employee hint', async (t) => {
  const { root } = mountProfile(t, baseUser({
    preferences: { yunzhijia_employee: true },
  }))
  await settle()

  const text = textOf(root)
  assert.ok(text.includes(EMPLOYEE_DESC),
    '员工账号必须显示「员工账号请从云之家进入…」文案')
  assert.ok(!text.includes(OIDC_DESC), '不应误用 OIDC 文案')
  assert.ok(!text.includes(NORMAL_DESC), '不应显示普通账号的改密说明')
  assert.equal(editButtons(root).length, 0, '员工账号不得渲染改密编辑入口')
})

test('AC-7 viewer account without the marker keeps the password editor', async (t) => {
  const { root } = mountProfile(t, baseUser())
  await settle()

  const text = textOf(root)
  assert.ok(text.includes(NORMAL_DESC), '普通 viewer 应保留原改密说明')
  assert.ok(!text.includes(EMPLOYEE_DESC), '无标记不得显示员工文案')
  assert.equal(editButtons(root).length, 1, '普通 viewer 必须保留改密入口')
})

test('AC-7 owner-like account without the marker keeps the password editor', async (t) => {
  const { root } = mountProfile(t, baseUser({
    can_access_all_tenants: true,
    preferences: {},
  }))
  await settle()

  assert.ok(textOf(root).includes(NORMAL_DESC))
  assert.equal(editButtons(root).length, 1,
    'owner/广域权限账号没有员工标记时不得隐藏改密入口')
})

test('AC-7 super admin without the marker keeps the password editor', async (t) => {
  const { root } = mountProfile(t, baseUser({
    is_system_admin: true,
    preferences: {},
  }))
  await settle()

  assert.ok(textOf(root).includes(NORMAL_DESC))
  assert.equal(editButtons(root).length, 1,
    '超管没有员工标记时不得隐藏改密入口')
})

test('AC-7 marker=false is treated as an ordinary account', async (t) => {
  const { root } = mountProfile(t, baseUser({
    preferences: { yunzhijia_employee: false },
  }))
  await settle()

  assert.ok(textOf(root).includes(NORMAL_DESC))
  assert.equal(editButtons(root).length, 1, '显式 false 不得隐藏改密入口')
})

test('AC-7 OIDC-only account keeps its existing hidden-password behaviour', async (t) => {
  const { root } = mountProfile(t, baseUser({
    preferences: { oidc_only_login: true },
  }))
  await settle()

  const text = textOf(root)
  assert.ok(text.includes(OIDC_DESC), 'OIDC 分支文案保持不变')
  assert.ok(!text.includes(EMPLOYEE_DESC), 'OIDC 账号不得被误判为云之家员工')
  assert.equal(editButtons(root).length, 0, 'OIDC 账号仍隐藏改密入口')
})
