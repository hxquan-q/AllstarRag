/**
 * T08 红阶段（single-space-per-account / AC-13）：useRoleLabel.formatRole
 * 读取真实 zh-CN 语言包，验证三个空间内角色的新显示名。
 *
 * 现状（预期红）：zh-CN `tenantMember.role.*` 仍是旧文案
 * owner=所有者 / admin=管理员 / viewer=访客；
 * 目标：owner=空间拥有者 / admin=普通管理员 / viewer=游客。
 * 新文案由 ② 实现时写入 locale；本文件只锁 zh-CN 精确值（票面命名）与
 * 6 语言键齐全，不替 ② 定义其余 5 语言的译文。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import * as vue from 'vue'
import { fixture } from '../test-utils/vueFixture.ts'
import zhCN from '../i18n/locales/zh-CN.ts'
import zhTW from '../i18n/locales/zh-TW.ts'
import enUS from '../i18n/locales/en-US.ts'
import jaJP from '../i18n/locales/ja-JP.ts'
import koKR from '../i18n/locales/ko-KR.ts'
import ruRU from '../i18n/locales/ru-RU.ts'

const SOURCE = readFileSync(new URL('./useRoleLabel.ts', import.meta.url), 'utf8')

const BUNDLES: Record<string, any> = {
  'zh-CN': zhCN,
  'zh-TW': zhTW,
  'en-US': enUS,
  'ja-JP': jaJP,
  'ko-KR': koKR,
  'ru-RU': ruRU,
}

function lookup(bundle: any, key: string): unknown {
  return key.split('.').reduce<any>(
    (node, part) => (node == null ? undefined : node[part]),
    bundle,
  )
}

/** 执行真实 useRoleLabel.ts 脚本，t 走真实 zh-CN 语言包（缺键回退键名，与实现一致）。 */
function loadFormatRole(t: TestContext) {
  const f = fixture(t)
  const exports = f.evaluate(SOURCE, {
    vue,
    'vue-i18n': {
      useI18n: () => ({
        t: (key: string) => {
          const value = lookup(zhCN, key)
          return typeof value === 'string' ? value : key
        },
      }),
    },
    '@/stores/auth': {
      useAuthStore: () => ({ user: null, memberships: [], tenant: null, effectiveTenantId: null }),
    },
  })
  assert.equal(typeof exports.useRoleLabel, 'function', 'useRoleLabel 必须可导出')
  return exports.useRoleLabel().formatRole as (role: string | null | undefined) => string
}

test('T08 formatRole(owner)=空间拥有者（AC-13，现状"所有者"，预期红）', (t) => {
  const formatRole = loadFormatRole(t)
  assert.equal(formatRole('owner'), '空间拥有者')
})

test('T08 formatRole(admin)=普通管理员（AC-13，现状"管理员"，预期红）', (t) => {
  const formatRole = loadFormatRole(t)
  assert.equal(formatRole('admin'), '普通管理员')
})

test('T08 formatRole(viewer)=游客（AC-13，现状"访客"，预期红）', (t) => {
  const formatRole = loadFormatRole(t)
  assert.equal(formatRole('viewer'), '游客')
})

test('formatRole 缺键回退原角色串、空值返回空串（基线，行为不变）', (t) => {
  const formatRole = loadFormatRole(t)
  assert.equal(formatRole('role-without-locale-key'), 'role-without-locale-key')
  assert.equal(formatRole(''), '')
  assert.equal(formatRole(null), '')
  assert.equal(formatRole(undefined), '')
})

for (const [locale, bundle] of Object.entries(BUNDLES)) {
  test(`T08 tenantMember.role.{owner,admin,viewer} 键齐全（${locale}，基线：键已存在）`, () => {
    for (const role of ['owner', 'admin', 'viewer']) {
      const value = lookup(bundle, `tenantMember.role.${role}`)
      assert.equal(typeof value, 'string', `${locale} 必须定义 tenantMember.role.${role}`)
      assert.ok(String(value).trim().length > 0, `${locale} tenantMember.role.${role} 文案不得为空`)
    }
  })
}
