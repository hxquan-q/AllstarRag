import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

/**
 * 客户未绑定 agent 弹窗 + T01 视觉修正的源码守卫（W1–W10 / C6）。
 *
 * 客户页面依赖 i18n / Pinia / 真实会话数据，node 环境不挂载整页；
 * 沿用仓库既有做法「读源码 + 正则断言真实分支」（见 customerShell.test.mjs）。
 * 真实渲染与交互（自动弹、X/Esc/遮罩关闭、发送不再弹、去绑定跳转）由
 * 05-tests.md 的 CDP 证据覆盖。
 */

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8')

const welcome = read('./CustomerWelcome.vue')
const dialog = read('./CustomerNoAgentDialog.vue')
const uiStore = read('../../stores/ui.ts')
const platform = read('../../views/platform/index.vue')
const inputField = read('../Input-field.vue')
const mobileCss = read('../../assets/theme/customer-mobile.css')

const LOCALES = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'ko-KR', 'ru-RU']
const locales = Object.fromEntries(
  LOCALES.map((name) => [name, read(`../../i18n/locales/${name}.ts`)]),
)

/** 去掉 CSS 注释，避免注释里的选择器/花括号干扰解析。 */
function stripCssComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

/** 读取 SFC 第一个 <style ...> 块的内容。 */
function styleBlock(source) {
  const open = source.search(/<style[^>]*>/)
  if (open < 0) return ''
  const start = source.indexOf('>', open) + 1
  const end = source.indexOf('</style>', start)
  return end < 0 ? '' : source.slice(start, end)
}

/** 从某个 `{` 起取配平的整段内容。 */
function extractBalancedBlock(text, openBraceIndex) {
  let depth = 0
  for (let index = openBraceIndex; index < text.length; index += 1) {
    if (text[index] === '{') depth += 1
    else if (text[index] === '}') {
      depth -= 1
      if (depth === 0) return text.slice(openBraceIndex + 1, index)
    }
  }
  return ''
}

/** 最小 CSS 块扫描器：返回每个 `选择器 { ... }`（跳过 at-rule 本身）。 */
function parseCssBlocks(css) {
  const text = stripCssComments(css)
  const stack = []
  const blocks = []
  let buffer = ''
  for (const char of text) {
    if (char === '{') {
      const prelude = buffer.trim()
      const block = {
        prelude,
        body: '',
        isAtRule: prelude.startsWith('@'),
        parentPrelude: stack.length ? stack[stack.length - 1].prelude : null,
      }
      stack.push(block)
      blocks.push(block)
      buffer = ''
    } else if (char === '}') {
      stack.pop()
      buffer = ''
    } else if (stack.length && !stack[stack.length - 1].isAtRule) {
      stack[stack.length - 1].body += char
    } else {
      buffer += char
    }
  }
  return blocks
}

/** 普通规则块（跳过 @media 本身），按选择器子串筛选。 */
function ruleBlocks(cssText, selectorPart) {
  return parseCssBlocks(cssText).filter(
    (block) => !block.isAtRule && block.prelude.includes(selectorPart),
  )
}

/** 取 `@media (<query>) { ... }` 的块内容。 */
function mediaBlock(cssText, query) {
  const clean = stripCssComments(cssText)
  const marker = `@media (${query})`
  const at = clean.indexOf(marker)
  assert.ok(at >= 0, `media block not found: ${marker}`)
  return extractBalancedBlock(clean, clean.indexOf('{', at))
}

/** 取 `key: { ... }` 的块内容（用于解析 i18n 对象字面量）。 */
function objectBlock(source, key, from = 0) {
  const at = source.indexOf(`${key}: {`, from)
  assert.ok(at >= 0, `object block not found: ${key}`)
  return extractBalancedBlock(source, source.indexOf('{', at))
}

// ---------------------------------------------------------------------------
// W5 / W6：uiStore 纯内存弹窗态 + 打开/关闭动作
// ---------------------------------------------------------------------------

test('W5/W6: uiStore owns a memory-only customerNoAgentDialogVisible flag with open/close', () => {
  assert.match(uiStore, /customerNoAgentDialogVisible: false,/)
  assert.match(
    uiStore,
    /openCustomerNoAgentDialog\(\) \{\s*this\.customerNoAgentDialogVisible = true\s*\}/,
  )
  assert.match(
    uiStore,
    /closeCustomerNoAgentDialog\(\) \{\s*this\.customerNoAgentDialogVisible = false\s*\}/,
  )
  // 关闭只影响当前这一次：状态不落 localStorage，不进 URL。
  assert.doesNotMatch(uiStore, /customerNoAgentDialogVisible[\s\S]{0,120}localStorage/)
})

// ---------------------------------------------------------------------------
// W5 / W9：欢迎页 watch 自动弹；不再渲染旧副标题/去绑定
// ---------------------------------------------------------------------------

test('W5: CustomerWelcome opens the dialog only when ready && !loadFailed && 0 lines', () => {
  assert.match(
    welcome,
    /watch\(\s*\(\) => \[productLineStore\.ready, productLineStore\.loadFailed, lines\.value\.length\] as const,\s*\(\[ready, failed, len\]\) => \{\s*if \(ready && !failed && len === 0\) uiStore\.openCustomerNoAgentDialog\(\)\s*\},\s*\{ immediate: true \},\s*\)/,
  )
  assert.match(welcome, /const uiStore = useUIStore\(\)/)
})

test('W9: CustomerWelcome keeps no subtitle / bind link / router navigation', () => {
  assert.doesNotMatch(welcome, /customer-welcome__subtitle/)
  assert.doesNotMatch(welcome, /customer-welcome__bind-btn/)
  assert.doesNotMatch(welcome, /router/)
  assert.doesNotMatch(welcome, /brand\.welcome\.emptyAction/)
  // 既有字标 / 卡片 / 示例问题 / 折叠阈值 4 仍保留。
  assert.match(welcome, /brand\/choose-agent-wordmark\.png/)
  assert.match(welcome, /v-for="line in visibleLines"/)
  assert.match(welcome, /v-for="question in samples"/)
  assert.match(welcome, /const COLLAPSED_LINE_COUNT = 4/)
})

// ---------------------------------------------------------------------------
// W7：Input-field createSession 最前面的守卫，位于 composerLocked 之前
// ---------------------------------------------------------------------------

test('W7: createSession blocks unbound customer sends before composerLocked and sends nothing', () => {
  const start = inputField.indexOf('const createSession = async (')
  assert.ok(start >= 0, 'createSession not found')
  const body = inputField.slice(start, start + 1200)

  const guard = /if \(props\.customerMode && productLineStore\.value\?\.ready && \(productLineStore\.value\?\.lines\.length \?\? 0\) === 0\) \{\s*uiStore\.openCustomerNoAgentDialog\(\);\s*return;\s*\}/
  assert.match(body, guard)
  assert.match(body, /uiStore\.openCustomerNoAgentDialog\(\)/)

  const guardAt = body.search(guard)
  const lockedAt = body.indexOf('if (props.composerLocked)')
  assert.ok(lockedAt > 0, 'composerLocked check not found in createSession')
  assert.ok(guardAt < lockedAt, 'no-agent guard must run before the composerLocked check')
  // 守卫在早退前不得发提示/调用发送链路。
  const guardedPart = body.slice(guardAt, lockedAt)
  assert.doesNotMatch(guardedPart, /MessagePlugin|emit\(|fetch\(|axios/)

  // 发送按钮与 Enter 都走 createSession，因此都会被守卫拦下（W7）。
  assert.match(inputField, /@click="createSession\(query\)"/)
  assert.match(inputField, /else void createSession\(val, delivery\)/)
})

// ---------------------------------------------------------------------------
// W6 / W8：弹窗关闭与去绑定
// ---------------------------------------------------------------------------

test('W6/W8: dialog closes through the store and binds via agentlines query', () => {
  assert.match(dialog, /:visible="uiStore\.customerNoAgentDialogVisible"/)
  assert.match(dialog, /:close-btn="true"/)
  assert.match(dialog, /:close-on-overlay-click="true"/)
  assert.match(dialog, /:close-on-esc-keydown="true"/)
  assert.match(dialog, /@close="close"/)

  assert.match(dialog, /function close\(\): void \{\s*uiStore\.closeCustomerNoAgentDialog\(\)\s*\}/)

  // W8：先跳设置页产线分区，再关闭弹窗。
  assert.match(
    dialog,
    /async function goBind\(\): Promise<void> \{\s*await router\.push\(\{ path: '\/platform\/settings', query: \{ section: 'agentlines' \} \}\)\s*close\(\)\s*\}/,
  )
  const goBind = dialog.slice(dialog.indexOf('async function goBind'))
  const pushAt = goBind.indexOf('router.push(')
  const closeAt = goBind.indexOf('close()', goBind.indexOf('await router.push('))
  assert.ok(pushAt >= 0 && closeAt > pushAt, 'goBind must close after navigation resolves')

  // 文案复用既有 key，不新增重复的关闭/去绑定文案。
  assert.match(dialog, /t\('brand\.noAgent\.title'\)/)
  assert.match(dialog, /t\('brand\.noAgent\.desc'\)/)
  assert.match(dialog, /t\('common\.close'\)/)
  assert.match(dialog, /t\('brand\.welcome\.emptyAction'\)/)
})

// ---------------------------------------------------------------------------
// W10 / C6：弹窗只挂客户壳，管理端不渲染
// ---------------------------------------------------------------------------

test('W10: platform mounts the dialog only when isCustomer (admin never gets it)', () => {
  assert.match(platform, /import CustomerNoAgentDialog from '@\/components\/customer\/CustomerNoAgentDialog\.vue'/)
  assert.match(platform, /<CustomerNoAgentDialog v-if="isCustomer" \/>/)
  assert.match(platform, /const isCustomer = computed\(\(\) => isCustomerViewer\(authStore\)\)/)

  // 管理端分支（v-if="!isCustomer" 的助手面板模板）内部不得出现该弹窗。
  const adminStart = platform.indexOf('<template v-if="!isCustomer">')
  assert.ok(adminStart >= 0, 'admin branch not found')
  const adminEnd = platform.indexOf('</template>', adminStart)
  const adminBranch = platform.slice(adminStart, adminEnd)
  assert.equal(adminBranch.includes('CustomerNoAgentDialog'), false, 'admin branch must not mount the dialog')
})

// ---------------------------------------------------------------------------
// W10：六语言文案齐全，并复用既有 emptyAction / common.close
// ---------------------------------------------------------------------------

test('W10: brand.noAgent.title/desc exist in all six locales and reuse existing keys', () => {
  for (const name of LOCALES) {
    const source = locales[name]
    const brand = objectBlock(source, 'brand')
    const noAgent = objectBlock(brand, 'noAgent', 0)
    assert.match(noAgent, /title:\s*'[^']+'/, `${name} brand.noAgent.title missing`)
    assert.match(noAgent, /desc:\s*'[^']{2,}'/, `${name} brand.noAgent.desc missing`)

    const welcomeBlock = objectBlock(brand, 'welcome', 0)
    assert.match(welcomeBlock, /emptyAction:\s*'[^']+'/, `${name} brand.welcome.emptyAction missing`)

    const common = objectBlock(source, 'common')
    assert.match(common, /close:\s*'[^']+'/, `${name} common.close missing`)
  }
})

// ---------------------------------------------------------------------------
// W1 / W2 / W3 / W4：T01 视觉修正的源码守卫
// ---------------------------------------------------------------------------

test('W1: .rich-input-container is border-box and 100% wide with a 960px cap', () => {
  const inputStyle = styleBlock(inputField)
  const container = ruleBlocks(inputStyle, '.rich-input-container').find(
    (block) => block.prelude.trim() === '.rich-input-container',
  )
  assert.ok(container, '.rich-input-container rule not found')
  assert.match(container.body, /box-sizing:\s*border-box;/)
  assert.match(container.body, /width:\s*100%;/)
  assert.match(container.body, /max-width:\s*960px;/)
})

test('W2/W3: wordmark keeps 246x66 and the -1.5deg optical tilt', () => {
  const welcomeStyle = styleBlock(welcome)
  const wordmark = ruleBlocks(welcomeStyle, '.customer-welcome__wordmark').find(
    (block) => block.prelude.trim() === '.customer-welcome__wordmark',
  )
  assert.ok(wordmark, 'wordmark rule not found')
  assert.match(wordmark.body, /width:\s*246px;/)
  assert.match(wordmark.body, /height:\s*66px;/)

  const img = ruleBlocks(welcomeStyle, '.customer-welcome__wordmark img').find(
    (block) => block.prelude.trim() === '.customer-welcome__wordmark img',
  )
  assert.ok(img, 'wordmark img rule not found')
  assert.match(img.body, /transform:\s*rotate\(-1\.5deg\);/)

  // 移动端按比例缩放但保持 246:66，不出现 0 宽规则。
  const welcome1023 = mediaBlock(welcomeStyle, 'max-width: 1023px')
  const mobileWordmark = ruleBlocks(welcome1023, '.customer-welcome__wordmark').find(
    (block) => block.prelude.trim() === '.customer-welcome__wordmark',
  )
  assert.ok(mobileWordmark, 'mobile wordmark rule not found')
  assert.match(mobileWordmark.body, /width:\s*min\(246px, 100%\);/)
  assert.match(mobileWordmark.body, /aspect-ratio:\s*246 \/ 66;/)
})

test('W3: mobile CSS forces welcome head/title to 100% width', () => {
  // 该文件的 1023 媒体查询不止一处，按父 at-rule 精确筛选，避免只命中第一段。
  const rule = parseCssBlocks(mobileCss).find(
    (block) =>
      !block.isAtRule &&
      block.parentPrelude === '@media (max-width: 1023px)' &&
      block.prelude.includes('html[data-customer="true"] .customer-welcome__head') &&
      block.prelude.includes('html[data-customer="true"] .customer-welcome__title'),
  )
  assert.ok(rule, 'welcome head/title mobile rule not found')
  assert.match(rule.body, /width:\s*100%;/)
})

test('W4: line cards are fixed 104px boxes with single-line name and two-line description', () => {
  const welcomeStyle = styleBlock(welcome)
  const card = ruleBlocks(welcomeStyle, '.line-card').find(
    (block) => block.prelude.trim() === '.line-card',
  )
  assert.ok(card, '.line-card rule not found')
  assert.match(card.body, /width:\s*320px;/)
  assert.match(card.body, /height:\s*104px;/)
  assert.match(card.body, /box-sizing:\s*border-box;/)

  const name = ruleBlocks(welcomeStyle, '.line-card__name').find(
    (block) => block.prelude.trim() === '.line-card__name',
  )
  assert.ok(name, '.line-card__name rule not found')
  assert.match(name.body, /white-space:\s*nowrap;/)
  assert.match(name.body, /text-overflow:\s*ellipsis;/)
  assert.match(name.body, /overflow:\s*hidden;/)

  const desc = ruleBlocks(welcomeStyle, '.line-card__desc').find(
    (block) => block.prelude.trim() === '.line-card__desc',
  )
  assert.ok(desc, '.line-card__desc rule not found')
  assert.match(desc.body, /display:\s*-webkit-box;/)
  assert.match(desc.body, /-webkit-line-clamp:\s*2;/)
  assert.match(desc.body, /overflow:\s*hidden;/)

  // 移动端卡片占满一行但仍沿用 104 的高度（不被 width:100% 覆盖掉高度）。
  const welcome1023 = mediaBlock(welcomeStyle, 'max-width: 1023px')
  const mobileCard = ruleBlocks(welcome1023, '.line-card').find(
    (block) => block.prelude.trim() === '.line-card',
  )
  assert.ok(mobileCard, 'mobile line-card rule not found')
  assert.match(mobileCard.body, /width:\s*100%;/)
  assert.doesNotMatch(mobileCard.body, /height:\s*(?:auto|100%);/)
})
