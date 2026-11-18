import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { brandCssVars, deriveBrandScale } from './colors.ts'
import { DEFAULT_BRAND } from './defaults.ts'
import { applyDocumentBrand, initCustomerBrandTheme } from './runtime.ts'

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

  // MutationObserver 桩：记录回调，由测试手动通知（等价浏览器的属性变化回调）。
  const observers: Array<{ callback: () => void; observing: boolean }> = []
  const fakeObserverClass = class {
    observing = false
    constructor(public callback: () => void) {
      observers.push(this)
    }
    observe() {
      this.observing = true
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
    notifyObservers: () => {
      for (const observer of observers) {
        if (observer.observing) observer.callback()
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
    initCustomerBrandTheme() // main.ts 同款装载路径（安装属性监听）
    applyDocumentBrand({ customer: false })
    // 切换前是亮色集：主档就是配置的主色。
    assert.equal(dom.cssVar('--td-brand-color-4'), DEFAULT_BRAND.primaryColor)
    dom.documentElement.setAttribute('theme-mode', 'dark')
    dom.notifyObservers() // 模拟浏览器把 theme-mode 属性变化通知 observer
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
 */
test('externally removing data-customer keeps brand vars on the document', () => {
  const dom = installDomStub()
  try {
    initCustomerBrandTheme() // main.ts 同款装载路径（安装属性监听）
    applyDocumentBrand({ customer: true }) // 先以客户身份写入天蓝
    assert.equal(dom.cssVar('--brand-main'), DEFAULT_BRAND.primaryColor)
    assert.equal(dom.documentElement.getAttribute('data-customer'), 'true')
    // 外部（非 runtime.ts 调用方）直接摘掉客户标记，observer 收到属性变化通知。
    dom.documentElement.removeAttribute('data-customer')
    dom.notifyObservers()
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
  ])
  // 锚定：派生引用结构保持现状（hover=-3、active=-5、主档 -4），②只许换值不许改结构。
  assert.equal(light['--brand-main'], 'var(--td-brand-color-4)')
  assert.equal(light['--td-brand-color'], 'var(--td-brand-color-4)')
  assert.equal(light['--td-brand-color-hover'], 'var(--td-brand-color-3)')
  assert.equal(light['--td-brand-color-active'], 'var(--td-brand-color-5)')
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
  ])
  // 锚定：暗块派生结构同样不动（主档 -6、hover=-5、active=-7）。
  assert.equal(dark['--brand-main'], 'var(--td-brand-color-6)')
  assert.equal(dark['--td-brand-color'], 'var(--td-brand-color-6)')
  assert.equal(dark['--td-brand-color-hover'], 'var(--td-brand-color-5)')
  assert.equal(dark['--td-brand-color-active'], 'var(--td-brand-color-7)')
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
