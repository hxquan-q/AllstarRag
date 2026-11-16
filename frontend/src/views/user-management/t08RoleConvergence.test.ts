/**
 * T08 红阶段（single-space-per-account / AC-1、AC-13、AC-14、AC-37）：
 * 前端角色显示与目录收敛的纯函数 / 配置 / i18n 断言。
 *
 * - userTableLabels：admin→普通管理员文案；contributor 并入普通管理员，不再出现独立「编辑」文案。
 * - types/useUserManagement：DirectoryRole 去掉 editor；EDITOR_BACKEND_ROLES 删除。
 * - settingsAccess：成员管理入口（分区 + 头像快捷入口）门槛 owner→admin。
 * - customerAccess / toolbox：复核基线（viewer 仍按客户；admin 可进工具箱管理分区）。
 * - 6 语言 i18n：userManagement.roles.admin / directory.admin 新键；owner 标签改「空间拥有者」。
 *
 * 目录渲染与请求参数的红断言在 UserManagement.page.test.ts / t44NoSpaceDirectory.test.ts。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { roleLabel } from './userTableLabels.ts'
import * as typesModule from './types.ts'
import {
  SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE,
  SETTINGS_SECTION_MIN_ROLE,
} from '../../config/settingsAccess.ts'
import { isCustomerViewer } from '../../config/customerAccess.ts'
import { canAccessToolboxSection } from '../../config/toolbox.ts'
import zhCN from '../../i18n/locales/zh-CN.ts'
import zhTW from '../../i18n/locales/zh-TW.ts'
import enUS from '../../i18n/locales/en-US.ts'
import jaJP from '../../i18n/locales/ja-JP.ts'
import koKR from '../../i18n/locales/ko-KR.ts'
import ruRU from '../../i18n/locales/ru-RU.ts'

const keyMirror = (key: string) => key

function lookup(bundle: any, key: string): unknown {
  return key.split('.').reduce<any>(
    (node, part) => (node == null ? undefined : node[part]),
    bundle,
  )
}

// ---------- 验收 2：userTableLabels ----------

test('T08 用户表角色文案：admin 映射 userManagement.roles.admin（现状映射 roles.editor，预期红）', () => {
  assert.equal(roleLabel('admin', keyMirror), 'userManagement.roles.admin')
})

test('T08 contributor 并入普通管理员显示，不再映射「编辑」键（现状 roles.editor，预期红）', () => {
  // AC-3：存量 contributor 迁移为 admin；残余行也按普通管理员显示，
  // 界面不再出现独立的「编辑」文案（AC-1）。
  assert.equal(roleLabel('contributor', keyMirror), 'userManagement.roles.admin')
  assert.notEqual(roleLabel('contributor', keyMirror), 'userManagement.roles.editor')
})

test('用户表 owner/viewer/system_admin/空串文案键不变（基线）', () => {
  assert.equal(roleLabel('owner', keyMirror), 'userManagement.roles.owner')
  assert.equal(roleLabel('viewer', keyMirror), 'userManagement.roles.viewer')
  assert.equal(roleLabel('system_admin', keyMirror), 'userManagement.roles.systemAdmin')
  assert.equal(roleLabel('', keyMirror), '—')
  assert.equal(roleLabel('legacy-unknown-role', keyMirror), 'legacy-unknown-role')
})

// ---------- 验收 3：types / useUserManagement 收敛 ----------

test('T08 EDITOR_BACKEND_ROLES 已删除（现状仍导出，预期红）', () => {
  // 用命名空间导入避免实现删除该导出后本文件在加载期崩掉。
  assert.equal('EDITOR_BACKEND_ROLES' in typesModule, false)
})

test('T08 DirectoryRole 不再含 editor，枚举为 owner/admin/viewer(+system_admin/no_space)（源码断言，预期红）', () => {
  const source = readFileSync(new URL('./types.ts', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /export type DirectoryRole[^\n]*'editor'/,
    'DirectoryRole 必须去掉 editor')
  assert.match(source, /'owner' \| 'admin' \| 'viewer' \| 'system_admin' \| 'no_space'/)
})

test('T08 useUserManagement 不再引用 editor 目录与 EDITOR_BACKEND_ROLES（源码断言，预期红）', () => {
  const source = readFileSync(new URL('./useUserManagement.ts', import.meta.url), 'utf8')
  assert.equal(source.includes('EDITOR_BACKEND_ROLES'), false)
  assert.equal(source.includes("'editor'"), false)
})

// ---------- 验收 5：settingsAccess / customerAccess / toolbox ----------

test('T08 成员管理入口门槛降为 admin：分区与头像快捷入口（现状 owner，预期红）', () => {
  assert.equal(SETTINGS_SECTION_MIN_ROLE['user-management'], 'admin')
  assert.equal(SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE['user-management'], 'admin')
  assert.equal(SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE.members, 'admin')
})

test('viewer 仍按客户视角，owner/admin 不按客户（基线复核，AC-13/验收 5）', () => {
  assert.equal(isCustomerViewer({ currentTenantRole: 'viewer' }), true)
  assert.equal(isCustomerViewer({ currentTenantRole: 'admin' }), false)
  assert.equal(isCustomerViewer({ currentTenantRole: 'owner' }), false)
  // 未水合/超管不误判为客户（fail-closed 基线）。
  assert.equal(isCustomerViewer({ currentTenantRole: '' }), false)
  assert.equal(isCustomerViewer({ currentTenantRole: 'viewer', isSystemAdmin: true }), false)
})

test('toolbox 门槛复核：admin 可进 skills/mcp，viewer 仅 browserconnection（基线）', () => {
  // 与 stores/auth.ts 的 ROLE_LEVEL 同序（viewer<contributor<admin<owner）。
  const levels: Record<string, number> = { viewer: 10, contributor: 20, admin: 30, owner: 40 }
  const access = (role: string | null) => ({
    currentTenantRole: role,
    canAccessAllTenants: false,
    hasRole: (min: string) => (levels[role ?? ''] ?? 0) >= (levels[min] ?? 0),
    isSupported: () => true,
  })
  assert.equal(canAccessToolboxSection('skills', access('admin')), true)
  assert.equal(canAccessToolboxSection('mcp', access('admin')), true)
  assert.equal(canAccessToolboxSection('skills', access('viewer')), false)
  assert.equal(canAccessToolboxSection('browserconnection', access('viewer')), true)
})

// ---------- 验收 6：6 语言 i18n ----------

const BUNDLES: Record<string, any> = {
  'zh-CN': zhCN,
  'zh-TW': zhTW,
  'en-US': enUS,
  'ja-JP': jaJP,
  'ko-KR': koKR,
  'ru-RU': ruRU,
}

for (const [locale, bundle] of Object.entries(BUNDLES)) {
  test(`T08 userManagement.roles.admin 与 directory.admin 键齐全（${locale}，现状缺键，预期红）`, () => {
    for (const key of ['userManagement.roles.admin', 'userManagement.directory.admin']) {
      const value = lookup(bundle, key)
      assert.equal(typeof value, 'string', `${locale} 缺 ${key}`)
      assert.ok(String(value).trim().length > 0, `${locale} ${key} 文案不得为空`)
    }
  })
}

test('T08 zh-CN 目录/表格 owner 标签改为「空间拥有者」（现状"普通管理员"，预期红）', () => {
  assert.equal(lookup(zhCN, 'userManagement.directory.owner'), '空间拥有者')
  assert.equal(lookup(zhCN, 'userManagement.roles.owner'), '空间拥有者')
})

test('zh-CN 目录/表格 viewer 标签为「游客」（基线复核）', () => {
  assert.equal(lookup(zhCN, 'userManagement.directory.viewer'), '游客')
  assert.equal(lookup(zhCN, 'userManagement.roles.viewer'), '游客')
})
