/**
 * T09：登录页「员工请从云之家进入」静态提示。
 *
 * 提示不依赖邮箱后缀/角色判断，只是 form-header 里的一行静态文案；
 * 这里用源码断言钉住模板位置，再用真实 zh-CN bundle 钉住文案。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import zhCN from '../../i18n/locales/zh-CN.ts'

const LOGIN_VUE = new URL('./Login.vue', import.meta.url)

test('login page renders the Yunzhijia employee hint as a static line in the form header', () => {
  const source = readFileSync(LOGIN_VUE, 'utf8')

  assert.match(
    source,
    /<p class="form-hint form-hint--yunzhijia">\{\{ \$t\('auth\.yunzhijia\.employeeHint'\) \}\}<\/p>/,
  )
  // 静态提示：同一个 <p> 上不能挂 v-if / v-else / v-show。
  const hintLine = source
    .split('\n')
    .find((line) => line.includes('form-hint--yunzhijia'))
  assert.ok(hintLine, 'hint line must exist')
  assert.doesNotMatch(hintLine, /v-(if|else|show)/)

  // 提示在 t-form 之前、form-header 内。
  const hintIndex = source.indexOf('form-hint--yunzhijia')
  const formIndex = source.indexOf('<t-form')
  assert.ok(hintIndex > 0 && formIndex > hintIndex, 'hint must precede the login form')
})

test('zh-CN bundle carries the requested employee hint text', () => {
  assert.equal(zhCN.auth.yunzhijia.employeeHint, '员工请从云之家进入')
})
