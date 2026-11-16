/**
 * 侧栏 TenantSelector 入口口径（T11 / AC-33）。
 *
 * 旧口径（T24 时代）断言「TenantSelector 仍由 menu.vue 渲染、纯系统管理员
 * 放行」；T11 把空间切换入口收敛到头像菜单（UserMenu 成为唯一入口），menu.vue
 * 删除侧栏 <TenantSelector> 入口与 import（仅此两处，上游存量 C10 不动）。
 * 本文件改为断言「不再渲染」，同时守住两条边界：
 * - TenantSelector.vue 文件本体保留（C10：不删不再渲染的上游文件）；
 * - menu.vue 仍挂载 UserMenu（头像菜单是空间切换唯一入口的宿主，
 *   超管门控在 UserMenu.showTenantSwitcher，宿主级用例见 UserMenu.t11.test.ts）。
 *
 * 建空间入口门控语义已迁 TenantSwitchPanel.test.ts（canCreateTenant
 * true/false 两条对照用例）；TenantSelector.createTenantEntry.test.ts
 * 继续对保留的文件本体生效（C10）。
 */
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { test } from 'node:test'

const source = readFileSync(new URL('./menu.vue', import.meta.url), 'utf8')

test('T11: menu.vue 不再渲染侧栏 TenantSelector（切换入口收敛至头像菜单，AC-33）', () => {
  assert.equal(
    source.includes('<TenantSelector'),
    false,
    'T11 后 menu.vue 不得再渲染 <TenantSelector>（头像菜单为唯一切换入口）',
  )
  assert.equal(
    source.includes("import TenantSelector from '@/components/TenantSelector.vue'"),
    false,
    'T11 后 menu.vue 不得再 import TenantSelector',
  )
  assert.equal(
    source.includes('TenantSelector'),
    false,
    'menu.vue 源文本应零处出现 TenantSelector（入口与 import 均已删除）',
  )
})

test('T11: TenantSelector.vue 本体保留（C10）且 UserMenu 挂载点不变（AC-33）', () => {
  assert.equal(
    existsSync(new URL('./TenantSelector.vue', import.meta.url)),
    true,
    'TenantSelector.vue 文件本体按 C10 保留（不再渲染但不删除上游文件）',
  )
  assert.ok(
    source.includes('<UserMenu'),
    'menu.vue 仍挂载 UserMenu：头像菜单承载空间切换唯一入口',
  )
})
