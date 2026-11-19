import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { brandCssVars, deriveBrandScale } from './colors.ts'
import { DEFAULT_BRAND } from './defaults.ts'
import { applyDocumentBrand, initCustomerBrandTheme, resetCustomerBrandThemeForTests } from './runtime.ts'

/**
 * admin-theme-blue T01 的 TDD 红阶段用例：管理端（非 data-customer）主色切品牌天蓝。
 * 写这些用例时 ② 尚未实现：全部用例预期失败（红），实现后应转绿；
 * 不要为了让用例通过而改这里的期望——期望口径来自 T01 验收条件 5 与 01-spec。
 *
 * runtime 用例跑在纯 Node（tsx --test）里，仓库没有 DOM 实现：
 * 照 useDocumentPictureInPicture.test.ts 的先例注入最小 document /
 * MutationObserver 桩，只实现 runtime.ts 实际用到的接口。
 * 期望值一律来自 DEFAULT_BRAND / brandCssVars / deriveBrandScale，
 * 即 C5 默认配置路径（Node 下 window 缺失，启动单例回落内置默认品牌）。
 */

/** 最小 DOM 桩：dataset.customer 与 data-customer attribute 双向同步。 */
function installDomStub() {
  const styleProps = new Map<string, string>()
  const attributes = new Map<string, string>()
  const dataset = {
    get customer() {
      return attributes.get('data-customer')
    },
    set customer(value: string | undefined) {
      if (value === undefined) attributes.delete('data-customer')
      else attributes.set('data-customer', value)
    },
  }
  const documentElement = {
    dataset,
    style: {
      setProperty(name: string, value: string) {
        styleProps.set(name, value)
      },
      removeProperty(name: string) {
        styleProps.delete(name)
      },
      getPropertyValue(name: string) {
        return styleProps.get(name) ?? ''
      },
    },
    getAttribute(name: string) {
      return attributes.get(name) ?? null
    },
    setAttribute(name: string, value: string) {
      attributes.set(name, value)
    },
    removeAttribute(name: string) {
      attributes.delete(name)
    },
  }
  const fakeDocument = {
    documentElement,
    title: '',
    // favicon 路径：querySelector 找不到就 createElement + head.appendChild。
    head: { appendChild() {} },
    querySelector: () => null,
    createElement: () => ({ rel: '', href: '' }),
  } as unknown as Document

  // MutationObserver 桩：记录回调与 observe 选项，由测试手动按属性名通知
  // （等价浏览器只把 attributeFilter 命中的属性变化回调给 observer）。
  type ObserverRecord = {
    callback: () => void
    observing: boolean
    attributeFilter?: string[]
  }
  const observers: ObserverRecord[] = []
  const fakeObserverClass = class {
    observing = false
    attributeFilter?: string[]
    constructor(public callback: () => void) {
      observers.push(this)
    }
    observe(_target: unknown, options?: { attributeFilter?: string[] }) {
      this.observing = true
      this.attributeFilter = options?.attributeFilter
    }
    disconnect() {
      this.observing = false
    }
  }
  const originals = { document: globalThis.document, observer: globalThis.MutationObserver }
  globalThis.document = fakeDocument
  globalThis.MutationObserver = fakeObserverClass as unknown as typeof MutationObserver

  return {
    documentElement,
    cssVar: (name: string) => styleProps.get(name) ?? '',
    // 当前桩里处于 observe 状态的 observer 数量：用于断言 init 真的安装了监听。
    observerCount: () => observers.filter((observer) => observer.observing).length,
    // 当前生效的 attributeFilter 列表：用于断言 observer 只监听 theme-mode（CP3-R2 L）。
    observedFilters: () => observers.filter((observer) => observer.observing).map((observer) => observer.attributeFilter),
    // 传 changedAttribute 时按 attributeFilter 过滤（模拟真实 MutationObserver）；
    // 不传则通知全部，供只关心「有通知」的既有用例沿用。
    notifyObservers: (changedAttribute?: string) => {
      for (const observer of observers) {
        if (!observer.observing) continue
        if (changedAttribute && observer.attributeFilter && !observer.attributeFilter.includes(changedAttribute)) {
          continue
        }
        observer.callback()
      }
    },
    restore: () => {
      globalThis.document = originals.document
      globalThis.MutationObserver = originals.observer
    },
  }
}

test('applyDocumentBrand({customer:false}) still writes brand vars for admin roles', () => {
  const dom = installDomStub()
  try {
    applyDocumentBrand({ customer: false })
    // 锚定：非客户不得挂客户版标记（客户专属圆角/阴影不得外溢，AC-3）。
    assert.notEqual(dom.documentElement.getAttribute('data-customer'), 'true')
    // 红点：品牌变量必须写入（现状走 removeBrandCssVars 清空）。
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color-4'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color'), DEFAULT_BRAND.actionColor)
  } finally {
    dom.restore()
  }
})

test('brand vars follow the dark set after theme-mode switches to dark', () => {
  const dom = installDomStub()
  try {
    // 先复位安装标记，保证本用例的 init 一定真正安装 observer（不依赖用例顺序）。
    resetCustomerBrandThemeForTests()
    initCustomerBrandTheme() // main.ts 同款装载路径（安装属性监听）
    assert.ok(dom.observerCount() > 0, 'observer 必须真正安装，否则下面的手动通知不构成有效验证')
    applyDocumentBrand({ customer: false })
    // 切换前是亮色集：主档就是配置的主色。
    assert.equal(dom.cssVar('--td-brand-color-4'), DEFAULT_BRAND.primaryColor)
    dom.documentElement.setAttribute('theme-mode', 'dark')
    dom.notifyObservers('theme-mode') // 模拟浏览器把 theme-mode 属性变化通知 observer
    // 红点：暗色集要重写入（现状 observer 对非客户走清空分支）。
    // 用 -1/-4 两档区分亮暗：亮 #e8f3f8/#0EA5E9，暗 #0EA5E920/#0b86be。
    const darkVars = brandCssVars(DEFAULT_BRAND, 'dark')
    assert.equal(dom.cssVar('--td-brand-color-1'), darkVars['--td-brand-color-1'] ?? '')
    assert.equal(dom.cssVar('--td-brand-color-4'), darkVars['--td-brand-color-4'] ?? '')
  } finally {
    dom.restore()
  }
})

test('removing the customer flag keeps brand vars on the document', () => {
  const dom = installDomStub()
  try {
    applyDocumentBrand({ customer: true }) // 登录页同款：先写入天蓝
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    // router 切非客户角色的同款调用（router/index.ts applyDocumentBrandForRole）。
    applyDocumentBrand({ customer: false })
    // 锚定：客户版标记确实被摘除（customer 选项只控标记）。
    assert.notEqual(dom.documentElement.getAttribute('data-customer'), 'true')
    // 红点：品牌变量应保留，不再被清空。
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color'), DEFAULT_BRAND.actionColor)
  } finally {
    dom.restore()
  }
})

/**
 * 票级复跑新增（队长 OQ-② 裁定）：外部脚本直接 removeAttribute('data-customer')
 * 时，observer 只按当前模式重写变量、不清空——旧实现有一个「外部摘标记就清空
 * 内联变量」的分支，T01 验收条件 2 要求移除该清理行为。
 * CP3-R2 L 变更：桩已按 attributeFilter 过滤，摘 data-customer 不再触发回调；
 * 变量仍在是因为没有任何清空路径（而非回调改写了值）。
 */
test('externally removing data-customer keeps brand vars on the document', () => {
  const dom = installDomStub()
  try {
    // 复位后安装，确保 observer 真的参与本用例（否则断言恒真、验收条件 2 证据链不成立）。
    resetCustomerBrandThemeForTests()
    initCustomerBrandTheme() // main.ts 同款装载路径（安装属性监听）
    assert.ok(dom.observerCount() > 0, 'observer 必须真正安装，否则外部摘标记分支未被验证')
    applyDocumentBrand({ customer: true }) // 先以客户身份写入天蓝
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.documentElement.getAttribute('data-customer'), 'true')
    // 外部（非 runtime.ts 调用方）直接摘掉客户标记；L 修复后 data-customer 不在
    // attributeFilter 内，桩按真实语义不回调 observer。
    dom.documentElement.removeAttribute('data-customer')
    dom.notifyObservers('data-customer')
    assert.notEqual(dom.documentElement.getAttribute('data-customer'), 'true')
    // 口径：变量不被清空，且仍按当前（亮色）模式保持天蓝集。
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color-4'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color'), DEFAULT_BRAND.actionColor)
  } finally {
    dom.restore()
  }
})

/**
 * CP3-R2 N1 补证：initCustomerBrandTheme 启动即写品牌变量，不依赖已挂
 * data-customer、也不依赖调用方先执行 applyDocumentBrand
 * （onboarding 等角色未判页面硬刷新后也能跟随 config.js，AC-2 缺口）。
 */
test('initCustomerBrandTheme writes brand vars at startup without a customer flag', () => {
  const dom = installDomStub()
  try {
    resetCustomerBrandThemeForTests()
    // 锚定前置：启动前既无客户标记，也没有任何调用方写入过变量。
    assert.notEqual(dom.documentElement.getAttribute('data-customer'), 'true')
    assert.equal(dom.cssVar('--brand-main'), '')
    initCustomerBrandTheme()
    // 锚定：仍然不挂 data-customer（客户版视觉不得外溢，AC-3）。
    assert.notEqual(dom.documentElement.getAttribute('data-customer'), 'true')
    // 红点（修复前）：只有已挂 data-customer 才写，这里变量应为空。
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color-4'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.cssVar('--td-brand-color'), DEFAULT_BRAND.actionColor)
  } finally {
    dom.restore()
  }
})

/**
 * CP3-R2 L 补证：observer 只监听 theme-mode，data-customer 变化不触发重写；
 * theme-mode 变化仍真实触发重写（证明 filter 不是「永远不触发」的空转）。
 */
test('brand observer listens only to theme-mode and still rewrites on theme-mode', () => {
  const dom = installDomStub()
  try {
    resetCustomerBrandThemeForTests()
    initCustomerBrandTheme()
    assert.ok(dom.observerCount() > 0, 'observer 必须真正安装')
    assert.deepEqual(dom.observedFilters(), [['theme-mode']], 'observer 应只监听 theme-mode（L 修复口径）')
    // 哨兵值：若 data-customer 变化误触发回调，会被 writeBrandCssVars 覆盖。
    dom.documentElement.style.setProperty('--td-brand-color', '#deadbe')
    dom.notifyObservers('data-customer')
    assert.equal(dom.cssVar('--td-brand-color'), '#deadbe', 'data-customer 变化不应触发变量重写')
    // theme-mode 变化仍应触发重写：切暗色后 -1 档落暗色集。
    dom.documentElement.setAttribute('theme-mode', 'dark')
    dom.notifyObservers('theme-mode')
    const darkVars = brandCssVars(DEFAULT_BRAND, 'dark')
    assert.equal(dom.cssVar('--td-brand-color-1'), darkVars['--td-brand-color-1'] ?? '')
    assert.equal(dom.cssVar('--td-brand-color'), DEFAULT_BRAND.actionColor)
  } finally {
    dom.restore()
  }
})

/**
 * theme.css 守卫：解析方式照 colors.test.ts 对 brand.css 的既有口径。
 * 期望值用 deriveBrandScale 输出；colors.test.ts 已锁死 brand.css 静态值
 * 与该输出一致，所以这里的 deepEqual 等价于「值与 brand.css 一致」（T01 验收条件 1）。
 * 注意主档输出是大写 #0EA5E9（normalizeHex 保留输入大小写），其余档为小写。
 */
function extractBlock(source: string, selector: string): string {
  const start = source.indexOf(selector)
  assert.notEqual(start, -1, `selector ${selector} should exist`)
  const open = source.indexOf('{', start)
  const close = source.indexOf('}', open)
  return source.slice(open + 1, close)
}

function cssVars(block: string, names: string[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const name of names) {
    const match = block.match(new RegExp(`${name}\\s*:\\s*([^;]+);`))
    if (match) out[name] = match[1]!.trim()
  }
  return out
}

const themeCss = readFileSync(new URL('../assets/theme/theme.css', import.meta.url), 'utf8')
const brandCss = readFileSync(new URL('../assets/theme/brand.css', import.meta.url), 'utf8')
const scaleNames = Array.from({ length: 10 }, (_, index) => `--td-brand-color-${index + 1}`)

// disabled 的期望直接取 brand.css 对应块的静态值，防止两个兜底文件漂移。
const brandLightDisabled = cssVars(extractBlock(brandCss, ':root:root[data-customer="true"] {'), [
  '--td-brand-color-disabled',
])['--td-brand-color-disabled']
const brandDarkDisabled = cssVars(
  extractBlock(brandCss, ':root:root[data-customer="true"][theme-mode="dark"] {'),
  ['--td-brand-color-disabled'],
)['--td-brand-color-disabled']

test('theme.css light brand scale equals the sky-blue static fallback', () => {
  const light = cssVars(extractBlock(themeCss, ':root:root[theme-mode="light"]'), [
    ...scaleNames,
    '--td-brand-color-disabled',
    '--brand-main',
    '--td-brand-color',
    '--td-brand-color-hover',
    '--td-brand-color-active',
    '--td-brand-color-focus',
  ])
  // 锚定：--brand-main 仍指向主档 -4（只换值不改结构）；交互色改为与
  // brand.css / 运行时 brandCssVars 同口径的具体值（AC-1 交互承载 #0284C7）。
  assert.equal(light['--brand-main'], 'var(--td-brand-color-4)')
  assert.equal(light['--td-brand-color'], '#0284C7')
  assert.equal(light['--td-brand-color-hover'], '#2093ce')
  assert.equal(light['--td-brand-color-active'], '#0274af')
  assert.equal(light['--td-brand-color-focus'], 'color-mix(in srgb, #0284C7 20%, transparent)')
  // 红点：色阶与 disabled 全部换为天蓝静态兜底（现状是绿色阶）。
  const expected = deriveBrandScale(DEFAULT_BRAND.primaryColor, 'light')
  for (const name of scaleNames) {
    assert.equal(light[name] ?? '', expected[name] ?? '', `${name} 应为天蓝静态兜底`)
  }
  assert.equal(light['--td-brand-color-disabled'] ?? '', brandLightDisabled ?? '')
})

test('theme.css dark brand scale equals the sky-blue static fallback', () => {
  const dark = cssVars(extractBlock(themeCss, ':root:root[theme-mode="dark"]'), [
    ...scaleNames,
    '--td-brand-color-disabled',
    '--brand-main',
    '--td-brand-color',
    '--td-brand-color-hover',
    '--td-brand-color-active',
    '--td-brand-color-focus',
  ])
  // 锚定：暗块 --brand-main 仍指向 -6；交互色同样改为具体值（暗色方向与 brand.css 一致）。
  assert.equal(dark['--brand-main'], 'var(--td-brand-color-6)')
  assert.equal(dark['--td-brand-color'], '#0284C7')
  assert.equal(dark['--td-brand-color-hover'], '#0277b3')
  assert.equal(dark['--td-brand-color-active'], '#2093ce')
  assert.equal(dark['--td-brand-color-focus'], 'color-mix(in srgb, #0284C7 28%, transparent)')
  // 红点：暗色阶与 disabled 换天蓝（现状是绿色阶，-1 档是 #06b04d20 带 alpha）。
  const expected = deriveBrandScale(DEFAULT_BRAND.primaryColor, 'dark')
  for (const name of scaleNames) {
    assert.equal(dark[name] ?? '', expected[name] ?? '', `${name} 应为天蓝静态兜底`)
  }
  assert.equal(dark['--td-brand-color-disabled'] ?? '', brandDarkDisabled ?? '')
})

// 亮/暗两块旧绿阶全量值 + 两个 disabled，去重后 13 个（#06b04d20 由 #06b04d 前缀覆盖）。
// 已核对它们只出现在品牌两块；success 语义绿 #00a870 不在清单内，不受影响。
const LEGACY_GREENS = [
  '#07c05f',
  '#06b04d',
  '#8ce0af',
  '#1a6b3b',
  '#e9f8ec',
  '#09f479',
  '#08dd6e',
  '#049b38',
  '#038626',
  '#027218',
  '#015e0d',
  '#004b05',
  '#a6fccf',
]

test('theme.css no longer contains legacy brand green values', () => {
  // includes 子串匹配天然覆盖 #06b04d20 这类带 alpha 的写法，避免按整值匹配漏网。
  const text = themeCss.toLowerCase()
  for (const legacy of LEGACY_GREENS) {
    assert.equal(text.includes(legacy), false, `theme.css 不应再出现旧品牌绿 ${legacy}`)
  }
})
