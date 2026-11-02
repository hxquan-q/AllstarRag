import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

/**
 * 客户移动断点（AC-1 / AC-22 / T02 断点口径）。
 *
 * 断点常量与 CSS 媒体查询必须同口径：统一按未缩放的视口宽度 < 1024，
 * 即 `(max-width: 1023px)`。这里既测纯函数边界，也用源码断言钉住
 * 「断点判断不乘 zoom」这条 T02 修过的回归线。
 *
 * 不能直接 `import './useResponsiveLayout'`：它运行时 import 了 `@/utils/zoom`，
 * 而本仓库的 tsconfig.json 是 solution-style（paths 在 tsconfig.app.json），
 * tsx 无法解析 `@/` 运行时别名（现有测试里的 `@/` 全是 import type）。
 * 因此沿用仓库既有做法（useCustomerAgent.test.ts / useModalShell.test.ts）：
 * 读源码、剥掉运行时 import、transpile 后用桩函数求值，测的仍是真实源码。
 */

const source = readFileSync(new URL('./useResponsiveLayout.ts', import.meta.url), 'utf8')

interface LayoutModule {
  MOBILE_LAYOUT_MAX_WIDTH: number
  MOBILE_LAYOUT_QUERY: string
  resolveIsMobileLayout: (width: number) => boolean
  useResponsiveLayout: () => {
    isMobileLayout: { value: boolean }
    viewportWidth: { value: number }
    viewportHeight: { value: number }
    isCoarsePointer: { value: boolean }
  }
}

function loadLayoutModule(): LayoutModule {
  const withoutImports = source
    .replace(/^import\s+.*$/gm, '')
    .replace(/^export\s+/gm, '')
  const compiled = ts.transpileModule(withoutImports, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
    },
  }).outputText

  const factory = new Function(
    'ref',
    'getCurrentScope',
    'onScopeDispose',
    'getRootZoom',
    `${compiled}
return { MOBILE_LAYOUT_MAX_WIDTH, MOBILE_LAYOUT_QUERY, resolveIsMobileLayout, useResponsiveLayout }`,
  )
  return factory(
    (value: unknown) => ({ value }),
    () => undefined,
    () => {},
    () => 1,
  ) as LayoutModule
}

const layout = loadLayoutModule()

test('breakpoint constants are the single source of truth for the CSS media query', () => {
  assert.equal(layout.MOBILE_LAYOUT_MAX_WIDTH, 1024)
  assert.equal(layout.MOBILE_LAYOUT_QUERY, '(max-width: 1023px)')

  // `width < 1024` 与 `max-width: 1023px` 在整数 CSS px 下严格等价。
  const mediaMax = layout.MOBILE_LAYOUT_QUERY.match(/max-width:\s*(\d+)px/)
  assert.ok(mediaMax, 'MOBILE_LAYOUT_QUERY must be a max-width media query')
  assert.equal(Number(mediaMax[1]), layout.MOBILE_LAYOUT_MAX_WIDTH - 1)
})

test('resolveIsMobileLayout flips exactly at 1023 / 1024', () => {
  assert.equal(layout.resolveIsMobileLayout(320), true)
  assert.equal(layout.resolveIsMobileLayout(768), true)
  assert.equal(layout.resolveIsMobileLayout(1023), true)
  assert.equal(layout.resolveIsMobileLayout(1024), false)
  assert.equal(layout.resolveIsMobileLayout(1280), false)
  assert.equal(layout.resolveIsMobileLayout(1440), false)
})

test('resolveIsMobileLayout is safe for zero / negative / NaN / Infinity input', () => {
  for (const value of [0, -1, -1024, NaN, -Infinity, Infinity]) {
    const result = layout.resolveIsMobileLayout(value)
    assert.equal(typeof result, 'boolean', `${String(value)} must return a boolean`)
  }
  // 实际语义：0 与负数低于断点 → 移动；NaN / Infinity 不满足 `< 1024` → 桌面。
  assert.equal(layout.resolveIsMobileLayout(0), true)
  assert.equal(layout.resolveIsMobileLayout(-100), true)
  assert.equal(layout.resolveIsMobileLayout(NaN), false)
  assert.equal(layout.resolveIsMobileLayout(Infinity), false)
})

test('useResponsiveLayout is SSR safe and returns desktop defaults without a window', () => {
  // node:test 环境下没有 DOM；hook 必须直接返回默认值而不是抛错。
  assert.equal(typeof window, 'undefined')
  assert.equal(typeof document, 'undefined')

  const state = layout.useResponsiveLayout()
  assert.equal(state.isMobileLayout.value, false)
  assert.equal(state.viewportWidth.value, 1440)
  assert.equal(state.viewportHeight.value, 900)
  assert.equal(state.isCoarsePointer.value, false)
})

test('source guard: breakpoint is compared with the unscaled viewport width', () => {
  // T02 回归线：不能再用 `1024 * zoom` 建查询，否则 CSS 与 JS 判断分叉。
  assert.doesNotMatch(source, /MOBILE_LAYOUT_MAX_WIDTH\s*\*/)
  assert.doesNotMatch(source, /scaledMaxWidthQuery/)
  assert.doesNotMatch(source, /1024\s*\*\s*zoom|zoom\s*\*\s*1024/)

  // 宽度取 window.innerWidth（与媒体查询同一口径），不再除以 zoom。
  assert.match(source, /viewportWidth\.value\s*=\s*window\.innerWidth/)
  assert.match(source, /return width < MOBILE_LAYOUT_MAX_WIDTH/)
  assert.match(source, /widthQuery = window\.matchMedia\(MOBILE_LAYOUT_QUERY\)/)
  // 高度仍按 zoom 换算（CSS px），只影响动态视口变量，不参与断点。
  assert.match(source, /viewportHeight\.value = rawHeight \/ zoom/)
})
