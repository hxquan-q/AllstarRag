/**
 * CP3 修复轮 2 前端证据（F24/F25/F26/F27/F33/F34）。
 * 只做源码/纯函数断言，避免依赖重量级挂载 fixture。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { shouldShowResourceOriginBadge } from '../../utils/card-list-badge.ts'

const userManagementSource = readFileSync(new URL('./UserManagement.vue', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('../settings/Settings.vue', import.meta.url), 'utf8')

test('F24：邀请管理/操作记录 Tab 由 KeepAlive 包裹，切走再切回保留状态', () => {
  assert.match(userManagementSource, /<KeepAlive>[\s\S]*?<InviteManagement[\s\S]*?<\/KeepAlive>/)
  assert.match(userManagementSource, /<KeepAlive>[\s\S]*?<OperationLog[\s\S]*?<\/KeepAlive>/)
})

test('F25：6 个语言包都包含 account / emptyHint 键', () => {
  for (const locale of ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'ko-KR', 'ru-RU']) {
    const source = readFileSync(new URL(`../../i18n/locales/${locale}.ts`, import.meta.url), 'utf8')
    assert.match(source, /account:/, `${locale} missing userManagement.columns.account`)
    assert.match(source, /emptyHint:/, `${locale} missing userManagement.states.emptyHint`)
  }
})

test('F26：墓碑创建者即使没有名字也必须展示徽章（AC-22）', () => {
  const base = { section: 'tenantOthers' as const, variant: 'creator' as const, showSectionHeaders: true }
  assert.equal(shouldShowResourceOriginBadge({ ...base, creatorName: '', creatorDeleted: true }), true)
  assert.equal(shouldShowResourceOriginBadge({ ...base, creatorName: '', creatorDeleted: false }), false)
  assert.equal(shouldShowResourceOriginBadge({ ...base, creatorName: 'Alice' }), true)
})

test('F27：纯超管在本空间重置 owner 时走系统级 reset 接口', () => {
  assert.match(
    userManagementSource,
    /isSystemAdmin\.value\s*&&\s*\([\s\S]*?scope\.value\s*!==\s*'current'[\s\S]*?role\s*===\s*'owner'[\s\S]*?\)/,
  )
  assert.match(userManagementSource, /:use-platform-api="usePlatformResetApi"/)
})

test('F34/T23：无租户角色的系统管理员默认落在 user-management', () => {
  assert.match(
    settingsSource,
    /if \(authStore\.isSystemAdmin && !authStore\.currentTenantRole\)[\s\S]*?find\(\(item\) => item\.key === 'user-management'\)[\s\S]*?return preferred\.key/,
  )
  assert.match(settingsSource, /visibleOrDefaultSection\(\)/)
})
