import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const source = readFileSync(new URL('./menu.vue', import.meta.url), 'utf8')

test('T24: 空间切换器对纯系统管理员放行', () => {
  // 允许写成 v-if 或 computed，只要有一行同时表达 canAccessAllTenants 与 isSystemAdmin 的「或」。
  const combined = source
    .split('\n')
    .find(
      (line) =>
        line.includes('canAccessAllTenants') &&
        line.includes('isSystemAdmin') &&
        line.includes('||'),
    )
  assert.ok(
    combined,
    'menu.vue must show the tenant switcher for canAccessAllTenants || isSystemAdmin',
  )
})

test('T24: 空间切换器仍由 menu.vue 渲染（结构未变）', () => {
  assert.ok(
    source.includes('<TenantSelector'),
    'menu.vue should keep rendering <TenantSelector>',
  )
})
