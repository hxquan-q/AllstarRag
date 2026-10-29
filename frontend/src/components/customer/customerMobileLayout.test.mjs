import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

/**
 * 客户移动端布局源码守卫（AC-1 / AC-5 / AC-6 / AC-8 / AC-11 / AC-12 / AC-13 /
 * AC-14 / AC-19 / AC-20 / AC-21 / AC-22）。
 *
 * 本仓库的客户页面依赖 i18n / Pinia / 真实会话数据，node 环境不挂载整页；
 * 仓库既有做法是「读源码 + 正则断言真实分支」（见 customerShell.test.mjs）。
 * 这里把移动端的关键契约钉住，防止后续改动把客户移动规则扩散到管理端/桌面。
 * 真实渲染、真机键盘与视觉验收统一记 BLOCKED，由 05-tests.md 交用户。
 */

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8')

const indexHtml = read('../../../index.html')
const mobileCss = read('../../assets/theme/customer-mobile.css')
const themeCss = read('../../assets/theme/theme.css')
const responsive = read('../../composables/useResponsiveLayout.ts')
const topBar = read('./CustomerTopBar.vue')
const menu = read('../menu.vue')
const chat = read('../../views/chat/index.vue')
const platform = read('../../views/platform/index.vue')
const inputField = read('../Input-field.vue')
const shell = read('../SettingsModalShell.vue')
const settings = read('../../views/settings/Settings.vue')
const references = read('../ChatReferencesDrawer.vue')
const welcome = read('./CustomerWelcome.vue')
const creatChat = read('../../views/creatChat/creatChat.vue')
const login = read('../../views/auth/Login.vue')
const onboarding = read('../../views/auth/WorkspaceOnboarding.vue')
const useFont = read('../../composables/useFont.ts')
const useFocusTrap = read('../../composables/useFocusTrap.ts')

// 其余四个 SettingsModalShell 消费者（管理端）不得传客户移动 props。
const adminShellConsumers = {
  'AgentEditorModal.vue': read('../../views/agent/AgentEditorModal.vue'),
  'AutomationEditor.vue': read('../../views/automation/AutomationEditor.vue'),
  'KnowledgeBaseEditorModal.vue': read('../../views/knowledge/KnowledgeBaseEditorModal.vue'),
  'OrganizationSettingsModal.vue': read('../../views/organization/OrganizationSettingsModal.vue'),
}

/** 去掉 CSS 注释，避免注释里的选择器/花括号干扰解析。 */
function stripCssComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

/**
 * 最小 CSS 块扫描器：返回每个 `选择器 { ... }`（跳过 @media 等 at-rule 本身）。
 * parentPrelude 用来判断该规则是否位于 @media 内。
 */
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
      // 普通规则块内部是声明；at-rule（@media）内部则继续累积下一个选择器。
      stack[stack.length - 1].body += char
    } else {
      buffer += char
    }
  }
  return blocks
}

function walkSourceFiles(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      walkSourceFiles(full, out)
    } else if (/\.(ts|vue|mjs|js)$/.test(entry.name) && !/\.test\./.test(entry.name)) {
      out.push(full)
    }
  }
  return out
}

/** 读取 SFC 第一个 <style ...> 块的内容（移动规则都在 scoped 样式里）。 */
function styleBlock(source) {
  const open = source.search(/<style[^>]*>/)
  if (open < 0) return ''
  const start = source.indexOf('>', open) + 1
  const end = source.indexOf('</style>', start)
  return end < 0 ? '' : source.slice(start, end)
}

/** 去掉 CSS 块注释与 Less 行注释，便于按选择器扫描。 */
function stripStyleComments(cssText) {
  return stripCssComments(cssText).replace(/(^|\s)\/\/[^\n]*/g, '$1')
}

/** 从某个 `{` 起取配平的整段内容（用于 @media 块）。 */
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

/** 取 `@media (<query>) { ... }` 的块内容。 */
function mediaBlock(cssText, query) {
  const clean = stripStyleComments(cssText)
  const marker = `@media (${query})`
  const at = clean.indexOf(marker)
  assert.ok(at >= 0, `media block not found: ${marker}`)
  return extractBalancedBlock(clean, clean.indexOf('{', at))
}

/** 粗粒度选择器提取：块内每个 `选择器 {` 的前导文本（够用于限定性断言）。 */
function selectorsIn(cssText) {
  const clean = stripStyleComments(cssText)
  return [...clean.matchAll(/([^{}]+)\{/g)]
    .map((match) => match[1].trim())
    .filter(Boolean)
}

/** 普通规则块（跳过 @media 本身），按选择器子串筛选。 */
function ruleBlocks(cssText, selectorPart) {
  return parseCssBlocks(cssText).filter(
    (block) => !block.isAtRule && block.prelude.includes(selectorPart),
  )
}

// ---------------------------------------------------------------------------
// 1. 作用域：移动 CSS 一条都不许漏到管理端
// ---------------------------------------------------------------------------

test('every customer-mobile.css rule is scoped to html[data-customer="true"]', () => {
  const blocks = parseCssBlocks(mobileCss)
  const ruleBlocks = blocks.filter((block) => !block.isAtRule)

  assert.ok(ruleBlocks.length >= 40, `expected many scoped rules, got ${ruleBlocks.length}`)

  for (const block of ruleBlocks) {
    assert.ok(block.prelude, 'CSS rule without a selector')
    for (const selector of block.prelude.split(',')) {
      assert.match(
        selector.trim(),
        /^html\[data-customer="true"\]/,
        `unscoped selector leaks to admin: ${block.prelude}`,
      )
    }

    if (block.parentPrelude === null) {
      // 媒体查询外只允许安全区自定义属性（见文件头约定）。
      const declarations = block.body.split(';').map((item) => item.trim()).filter(Boolean)
      assert.ok(declarations.length > 0, `empty top-level block: ${block.prelude}`)
      for (const declaration of declarations) {
        assert.match(
          declaration,
          /^--app-safe-(top|bottom|left|right):/,
          `only safe-area variables may live outside @media: ${declaration}`,
        )
      }
    } else {
      assert.match(
        block.parentPrelude,
        /^@media /,
        `rule must live inside a media query: ${block.prelude}`,
      )
    }
  }

  // 断点与 JS 常量同口径，且移动样式不得出现 1024px 的媒体查询。
  assert.match(mobileCss, /@media \(max-width: 1023px\)/)
  assert.doesNotMatch(mobileCss, /@media \(max-width: 1024px\)/)
})

test('index.html opts into safe-area insets (AC-4)', () => {
  assert.match(indexHtml, /<meta\s+name="viewport"[^>]*viewport-fit=cover/)
})

// ---------------------------------------------------------------------------
// 2. 断点常量唯一 + 其他文件不写裸 1024 断点
// ---------------------------------------------------------------------------

test('MOBILE_LAYOUT_MAX_WIDTH is declared in exactly one file', () => {
  const srcRoot = fileURLToPath(new URL('../../', import.meta.url))
  const declaring = walkSourceFiles(srcRoot).filter((file) =>
    /MOBILE_LAYOUT_MAX_WIDTH\s*=/.test(readFileSync(file, 'utf8')),
  )
  const relative = declaring.map((file) => file.slice(srcRoot.length).replace(/\\/g, '/'))
  assert.deepEqual(relative, ['composables/useResponsiveLayout.ts'])
  assert.match(responsive, /export const MOBILE_LAYOUT_MAX_WIDTH = 1024/)
})

test('mobile consumers do not hardcode a 1024 breakpoint', () => {
  const consumers = {
    'CustomerTopBar.vue': topBar,
    'menu.vue': menu,
    'chat/index.vue': chat,
    'platform/index.vue': platform,
    'Input-field.vue': inputField,
    'SettingsModalShell.vue': shell,
    'Settings.vue': settings,
    'ChatReferencesDrawer.vue': references,
  }
  for (const [name, source] of Object.entries(consumers)) {
    assert.doesNotMatch(
      source,
      /(?:innerWidth|clientWidth|width)\s*(?:<|<=|>|>=)\s*1024\b/,
      `${name} must use MOBILE_LAYOUT_MAX_WIDTH instead of a bare 1024 breakpoint`,
    )
    assert.doesNotMatch(
      source,
      /\b1024\s*(?:<|<=|>|>=)\s*(?:window\.)?(?:innerWidth|clientWidth|width)\b/,
      `${name} must use MOBILE_LAYOUT_MAX_WIDTH instead of a bare 1024 breakpoint`,
    )
  }
})

// ---------------------------------------------------------------------------
// 3. 汉堡 / 抽屉 / 遮罩：只在客户 + 移动
// ---------------------------------------------------------------------------

test('CustomerTopBar renders the hamburger only when isMobileLayout (AC-5 / AC-21)', () => {
  assert.match(topBar, /<button v-if="isMobileLayout" type="button" class="customer-topbar__menu"/)
  assert.doesNotMatch(topBar, /<button type="button" class="customer-topbar__menu"/)

  const button = topBar.slice(
    topBar.indexOf('class="customer-topbar__menu"'),
    topBar.indexOf('</button>'),
  )
  assert.match(button, /:aria-label="t\('menu\.expandSidebar'\)"/)
  assert.match(button, /:aria-expanded="uiStore\.mobileDrawerOpen"/)
  assert.match(button, /@click="uiStore\.toggleMobileDrawer\(\)"/)

  // 44×44 触控目标。
  assert.match(topBar, /\.customer-topbar__menu \{[\s\S]*?width: 44px;[\s\S]*?height: 44px;/)

  // 客户顶栏本身也只挂在客户分支（管理端仍走 ChatHeader）。
  assert.match(chat, /<CustomerTopBar v-if="!embeddedMode && isCustomer" :title="currentSessionTitle">/)
})

test('menu drawer classes require customer + mobile, desktop fold/resize stays intact (AC-6 / AC-8)', () => {
  assert.match(menu, /const mobileDrawer = computed\(\(\) => isCustomer\.value && isMobileLayout\.value\)/)
  assert.match(menu, /'aside_box--mobile-drawer': mobileDrawer,/)
  assert.match(menu, /'aside_box--mobile-open': mobileDrawer && uiStore\.mobileDrawerOpen,/)

  // 抽屉语义与键盘关闭。
  assert.match(menu, /:role="mobileDrawer \? 'dialog' : undefined"/)
  assert.match(menu, /:aria-modal="mobileDrawer \? 'true' : undefined"/)
  assert.match(menu, /if \(!mobileDrawerActive\.value \|\| event\.key !== 'Escape'\) return/)
  assert.match(menu, /useFocusTrap\(mobileDrawerRef, mobileDrawerActive,/)

  // 桌面能力保留：拖拽手柄只在非抽屉态渲染，浮出栏 CSS 在移动态隐藏。
  assert.match(menu, /<PanelResizeHandle v-if="!mobileDrawer"/)
  assert.match(mobileCss, /html\[data-customer="true"\] \.sidebar-float-rail \{\s*display: none;/)
})

test('platform shell mounts scrim / inert / scroll lock only for customer mobile (AC-1 / AC-6 / C6)', () => {
  assert.match(platform, /const isCustomerMobile = computed\(\(\) => isCustomer\.value && isMobileLayout\.value\)/)
  assert.match(platform, /const mobileDrawerOpen = computed\(\(\) => isCustomerMobile\.value && uiStore\.mobileDrawerOpen\)/)
  assert.match(platform, /:inert="mobileDrawerOpen" :aria-hidden="mobileDrawerOpen \? 'true' : undefined"/)
  assert.match(platform, /<div v-if="mobileDrawerOpen" class="customer-mobile-scrim" aria-hidden="true"/)
  assert.match(platform, /document\.documentElement\.classList\.toggle\('customer-mobile-drawer-open', open\)/)

  // 跨断点回到桌面时复位抽屉，避免遮罩/滚动锁残留（AC-1）。
  assert.match(platform, /if \(!mobile && uiStore\.mobileDrawerOpen\) uiStore\.closeMobileDrawer\(\)/)
  // 路由变化（含点历史会话）自动关闭（AC-6 / AC-7）。
  assert.match(platform, /watch\(\(\) => route\.fullPath, \(\) => \{\s*if \(uiStore\.mobileDrawerOpen\) uiStore\.closeMobileDrawer\(\)/)
})

// ---------------------------------------------------------------------------
// 4. 对话页：沙箱裁剪只作用于客户移动；管理员分支不含移动条件
// ---------------------------------------------------------------------------

test('chat hides the sandbox toggle only inside the customer mobile actions slot (AC-13)', () => {
  assert.match(chat, /'is-mobile': customerMobile,/)
  assert.match(chat, /const customerMobile = computed\(\(\) => isCustomer\.value && isMobileLayout\.value\)/)

  const customerTopBar = chat.slice(
    chat.indexOf('<CustomerTopBar v-if="!embeddedMode && isCustomer"'),
    chat.indexOf('</CustomerTopBar>'),
  )
  assert.ok(customerTopBar.includes('<template v-if="!customerMobile">'))
  assert.match(customerTopBar, /<template v-if="!customerMobile">[\s\S]*?sandbox-header-toggle/)

  // 管理员 `.chat-topbar` 分支（客户 v-if 的 v-else）不得被移动条件包住。
  const adminTopBar = chat.slice(
    chat.indexOf('<div v-else-if="!embeddedMode" class="chat-topbar">'),
    chat.indexOf('<ChildAgentTabBar'),
  )
  assert.ok(adminTopBar.length > 0, 'admin chat-topbar branch not found')
  for (const token of ['customerMobile', 'isCustomerMobile', 'isMobileLayout', 'is-mobile', 'MOBILE_LAYOUT']) {
    assert.equal(
      adminTopBar.includes(token),
      false,
      `admin chat-topbar branch must not reference mobile state: ${token}`,
    )
  }
})

test('Input-field applies mobile styles only for customer mode on narrow screens (AC-11 / C6)', () => {
  assert.match(inputField, /const isCustomerMobile = computed\(\(\) => props\.customerMode && isMobileLayout\.value\)/)
  assert.match(inputField, /'is-mobile': isCustomerMobile/)

  // 客户分支契约不变：只有产线选择器与发送按钮，管理员分支仍保留附件等入口。
  assert.match(inputField, /<template v-if="customerMode">/)
  assert.match(inputField, /<MorphIcon v-if="customerMode" class="send-btn__morph"/)
  assert.match(inputField, /<AttachmentUpload v-if="!customerMode"/)
})

// ---------------------------------------------------------------------------
// 5. 设置：移动全屏 + 二级导航；桌面/管理端结构不变
// ---------------------------------------------------------------------------

test('SettingsModalShell keeps the desktop structure when mobileLayout=false (AC-14 / AC-21 / C6)', () => {
  assert.match(shell, /mobileLayout\?: boolean/)
  assert.match(shell, /mobileShowNav\?: boolean/)

  // 移动结构整体受 v-if="mobileLayout" 控制，桌面走原 v-else 结构。
  assert.match(shell, /<div v-if="mobileLayout" class="settings-mobile-header">/)
  assert.match(shell, /<div v-if="mobileLayout" class="settings-container settings-container--mobile">/)
  assert.match(shell, /<div v-else class="settings-container">/)
  assert.match(shell, /<aside class="settings-sidebar">/)

  // 默认值：mobileLayout=false 保证旧消费者 DOM 不变；
  // mobileShowNav 默认 true（02-plan / T04-notes 口径：移动态默认先显示一级列表）。
  assert.match(shell, /mobileLayout: false,/)
  assert.match(shell, /mobileShowNav: true,/)

  // 只有客户设置页传移动 props；四个管理端消费者不传。
  assert.match(settings, /:mobile-layout="mobileLayout" :mobile-show-nav="mobileShowNav"/)
  for (const [name, source] of Object.entries(adminShellConsumers)) {
    assert.equal(source.includes('mobile-layout'), false, `${name} must not pass mobile-layout`)
    assert.equal(source.includes('mobile-show-nav'), false, `${name} must not pass mobile-show-nav`)
  }
})

test('Settings.vue mobile nav: internal entry starts on the list, deep links enter the section (AC-14)', () => {
  assert.match(settings, /const mobileLayout = computed\(\(\) => isCustomerViewerActive\.value && isMobileLayout\.value\)/)
  assert.match(settings, /const mobileShowNav = ref\(true\)/)

  // 点一级导航进入分区内容；返回按钮回到列表。
  assert.match(settings, /if \(mobileLayout\.value\) mobileShowNav\.value = false/)
  assert.match(settings, /@mobile-back="mobileShowNav = true"/)

  // CP3 F2：移动层级以 URL ?section= 为主判据并随其变化，不再用 showSettingsModal 推导。
  assert.doesNotMatch(settings, /directToSection/)
  assert.match(
    settings,
    /watch\(\s*\(\) => \[visible\.value, route\.path, route\.query\.section\] as const,\s*\(\[isVisible, path, section\]\) => \{\s*if \(!isVisible \|\| path !== '\/platform\/settings'\) return\s*if \(!mobileLayout\.value\) return\s*mobileShowNav\.value = !\(typeof section === 'string' && section !== ''\)/,
  )
  // 客户移动态没有 ?section= 时停在分区列表，不自动补默认分区（否则列表会被拉进内容）。
  assert.match(
    settings,
    /if \(typeof section !== 'string'\) \{\s*\/\/[\s\S]*?if \(mobileLayout\.value\) return\s*syncSettingsRoute\(currentSection\.value \|\| 'general'\)/,
  )
})

test('Settings.vue mobile nav items are keyboard / screen-reader operable (CP3 F4)', () => {
  // 移动态补 role/tabindex/aria-current；桌面态全部 undefined（AC-21 / C6）。
  assert.match(settings, /:role="mobileLayout \? 'button' : undefined"/)
  assert.match(settings, /:tabindex="mobileLayout \? 0 : undefined"/)
  assert.match(settings, /:aria-current="mobileLayout && currentSection === item\.key \? 'page' : undefined"/)
  assert.match(settings, /@keydown="handleNavKeydown\(item, \$event\)"/)

  // Enter / Space 等价于点击；桌面态直接返回，不影响管理端。
  assert.match(
    settings,
    /const handleNavKeydown = \(item: NavItem, event: KeyboardEvent\) => \{\s*if \(!mobileLayout\.value\) return\s*if \(event\.key !== 'Enter' && event\.key !== ' '\) return\s*event\.preventDefault\(\)\s*handleNavClick\(item\)/,
  )
})

// ---------------------------------------------------------------------------
// 6. 引用面板：只有显式 overlayBreakpoint 才全屏；chat 仅客户移动传 1024
// ---------------------------------------------------------------------------

test('ChatReferencesDrawer fills the viewport only on explicit opt-in (AC-12 / C6)', () => {
  assert.match(
    references,
    /const fillViewport = computed\(\(\) => useOverlay\.value && props\.overlayBreakpoint !== undefined\)/,
  )
  // 全屏时隐藏拖拽手柄；桌面 / embed 保持原宽度偏好与手柄。
  assert.match(references, /v-if="!fillViewport && maxPanelWidth > minPanelWidth"/)
  // CP3 F3：全屏宽度用 100%，不用 100vw（根字号 zoom 会把 vw 再乘一次）。
  assert.match(references, /width: fillViewport\.value \? '100%' : `\$\{panelWidth\.value\}px`/)
  assert.doesNotMatch(references, /fillViewport\.value \? '100vw'/)

  const overlayPanel = ruleBlocks(mobileCss, '.chat-references-panel.is-overlay')
    .find((block) => block.body.includes('max-width: 100%;'))
  assert.ok(overlayPanel, 'references overlay panel rule not found')
  assert.match(overlayPanel.body, /width: 100%;/)
  assert.match(overlayPanel.body, /max-width: 100%;/)
  assert.doesNotMatch(overlayPanel.body, /100(vw|dvh)/)

  // 只有 chat 页传 overlay-breakpoint，且只在客户移动态传断点常量。
  assert.match(
    chat,
    /<ChatReferencesDrawer :overlay-breakpoint="customerMobile \? MOBILE_LAYOUT_MAX_WIDTH : undefined" \/>/,
  )
  assert.match(chat, /import \{ useResponsiveLayout, MOBILE_LAYOUT_MAX_WIDTH \} from '@\/composables\/useResponsiveLayout'/)
})

// ---------------------------------------------------------------------------
// 7. 移动 CSS 细节：横向滚动、输入字号、触控目标、动效降级
// ---------------------------------------------------------------------------

test('mobile CSS makes tables scroll in place (AC-2 / AC-10)', () => {
  const tableStart = mobileCss.indexOf(
    'html[data-customer="true"] .chat.customer-shell .msg_list table',
  )
  assert.ok(tableStart >= 0, 'customer table rule not found')
  const displayBlock = mobileCss.indexOf('display: block;', tableStart)
  assert.ok(displayBlock > tableStart, 'table rule must switch to display:block')
  const tableRule = mobileCss.slice(tableStart, displayBlock + 200)
  assert.match(tableRule, /max-width: 100%;/)
  assert.match(tableRule, /overflow-x: auto;/)
})

test('mobile CSS enforces 16px inputs and 44px touch targets (AC-10 / AC-11 / AC-20)', () => {
  // 输入字号用令牌（theme.css 里 --app-text-xl: 16px），避免 iOS 聚焦缩放。
  assert.match(themeCss, /--app-text-xl: 16px;/)
  assert.match(
    mobileCss,
    /html\[data-customer="true"\] textarea,\s*html\[data-customer="true"\] input,\s*html\[data-customer="true"\] \.t-textarea__inner \{\s*font-size: var\(--app-text-xl\);/,
  )

  assert.match(
    mobileCss,
    /html\[data-customer="true"\] \.control-btn,\s*html\[data-customer="true"\] \.send-btn,\s*html\[data-customer="true"\] \.customer-topbar button,\s*html\[data-customer="true"\] \.menu_item \{\s*min-width: 44px;\s*min-height: 44px;/,
  )
})

test('mobile motion respects prefers-reduced-motion and dialogs are labelled (AC-20)', () => {
  assert.match(mobileCss, /@media \(max-width: 1023px\) and \(prefers-reduced-motion: reduce\)/)
  assert.match(mobileCss, /transition: none;/)
  assert.match(shell, /role="dialog" aria-modal="true"/)
  assert.match(shell, /:aria-label="title"/)
  assert.match(references, /role="complementary"/)
})

// ---------------------------------------------------------------------------
// 8. CP3 修复轮（F1-F15）守卫：zoom 视口单位、键盘可达、安全区、状态清理
// ---------------------------------------------------------------------------

test('CP3 F1: settings mobile panel fills its fixed overlay with 100% (no viewport units)', () => {
  const panel = ruleBlocks(mobileCss, '.settings-modal--mobile')
    .find((block) => block.body.includes('max-width: none;'))
  assert.ok(panel, 'settings mobile panel rule not found')
  assert.match(panel.body, /width: 100%;/)
  assert.match(panel.body, /height: 100%;/)
  assert.doesNotMatch(panel.body, /100(vw|dvh|vh)/)
})

test('CP3 F5: Login goes single-column with 16px inputs at <=1023 and keeps legacy breaks', () => {
  const loginStyle = styleBlock(login)
  const login1023 = mediaBlock(loginStyle, 'max-width: 1023px')

  assert.match(login1023, /\.login-layout \{\s*flex-direction: column;/)
  assert.match(login1023, /\.form-section \{\s*order: -1;/)
  assert.match(
    login1023,
    /\.form-content :deep\(\.t-input \.t-input__inner\) \{\s*font-size: var\(--app-text-xl\);/,
  )

  // 既有 768 / 480 与 T05 的 360 段仍在，且顺序为 1023 → 768 → 480 → 360。
  const order = ['max-width: 1023px', 'max-width: 768px', 'max-width: 480px', 'max-width: 360px']
    .map((query) => loginStyle.indexOf(`@media (${query})`))
  for (const [index, position] of order.entries()) {
    assert.ok(position >= 0, `Login missing media block: ${order[index]}`)
    if (index > 0) assert.ok(position > order[index - 1], `Login breakpoint order regressed at ${order[index]}`)
  }
})

test('CP3 F6: customer mobile topbar / message list / input respect horizontal safe areas', () => {
  const safeLeft = 'max(12px, var(--app-safe-left))'
  const safeRight = 'max(12px, var(--app-safe-right))'
  const targets = [
    ['.customer-topbar', 'customer topbar'],
    ['.chat.customer-shell.is-mobile .msg_list', 'message list'],
    ['.answers-input.is-mobile', 'input column'],
  ]
  for (const [selector, label] of targets) {
    const block = ruleBlocks(mobileCss, selector).find((item) => item.body.includes('padding-left'))
    assert.ok(block, `${label} safe-area rule not found`)
    assert.ok(block.body.includes(`padding-left: ${safeLeft};`), `${label} missing left safe area`)
    assert.ok(block.body.includes(`padding-right: ${safeRight};`), `${label} missing right safe area`)
  }
})

test('CP3 F7: welcome empty-state retry chip joins the 44px rule inside <=1023 media block', () => {
  const welcomeStyle = styleBlock(welcome)
  const touchRule = parseCssBlocks(welcomeStyle).find(
    (block) => !block.isAtRule && block.prelude.includes('.customer-welcome__empty .sample-chip'),
  )
  assert.ok(touchRule, 'empty-state retry chip rule not found')
  // F20：44px 规则必须落在 @media (max-width: 1023px) 内，而不是写成全局规则。
  assert.equal(
    touchRule.parentPrelude,
    '@media (max-width: 1023px)',
    'retry chip 44px rule must live inside the mobile media query',
  )
  assert.match(touchRule.body, /min-height: 44px;/)

  const welcome1023 = mediaBlock(welcomeStyle, 'max-width: 1023px')
  const scopedRule = ruleBlocks(welcome1023, '.customer-welcome__empty .sample-chip').find(
    (block) => block.body.includes('min-height: 44px;'),
  )
  assert.ok(scopedRule, 'retry chip 44px rule missing from the 1023px block')
})

test('CP3 F8: useFont comments no longer instruct rebuilding media queries', () => {
  assert.doesNotMatch(useFont, /重建按缩放/)
  assert.doesNotMatch(useFont, /否则字号放大后断点会误判/)
  assert.match(useFont, /字号变化只重测动态视口高度；\s*\/\/\s*断点固定 1023px，不随 zoom 重建媒体查询。/)
  assert.match(useFont, /通知布局层重测动态视口高度；断点固定 1023px，不随 zoom 重建。/)
})

test('CP3 F9/F10: coarse pointer sync, viewport var cleanup and focus-trap reset', () => {
  assert.match(responsive, /let activeInstances = 0/)
  assert.match(
    responsive,
    /function onCoarsePointerChange\(event: MediaQueryListEvent\) \{\s*isCoarsePointer\.value = event\.matches\s*\}/,
  )
  assert.match(responsive, /coarseQuery\.addEventListener\('change', onCoarsePointerChange\)/)
  assert.match(responsive, /coarseQuery\.removeEventListener\('change', onCoarsePointerChange\)/)

  // F20 同步（F19 新契约）：减量包在 tracksInstance 内，归零时同时清掉宽度与高度变量。
  assert.match(
    responsive,
    /if \(tracksInstance\) \{\s*activeInstances = Math\.max\(0, activeInstances - 1\)\s*if \(activeInstances === 0\) \{\s*root\.style\.removeProperty\(VIEWPORT_WIDTH_VAR\)\s*root\.style\.removeProperty\(VIEWPORT_HEIGHT_VAR\)\s*\}\s*\}/,
  )

  // 焦点陷阱卸载时复位内部状态，避免复用实例残留。
  const focusDispose = useFocusTrap.slice(useFocusTrap.indexOf('onScopeDispose('))
  assert.match(
    focusDispose,
    /document\.removeEventListener\('keydown', onKeydown, true\)[\s\S]*?isActive = false\s*lastFocused = null/,
  )
})

test('CP3 F11/F12/F14/F15: dead class gone, drawer closes on same-route nav, shell-only rules', () => {
  // F11：死 class 已从全仓删除。
  const srcRoot = fileURLToPath(new URL('../../', import.meta.url))
  const deadClassOffenders = walkSourceFiles(srcRoot).filter((file) =>
    readFileSync(file, 'utf8').includes('main--customer-mobile'),
  )
  assert.deepEqual(deadClassOffenders, [])

  // F12：菜单与会话导航在同路径点击时也显式关抽屉。
  const menuClick = menu.slice(
    menu.indexOf('const handleMenuClick = async (path: string) => {'),
    menu.indexOf('const getCurrentKbId'),
  )
  assert.ok(menuClick.includes('uiStore.closeMobileDrawer()'), 'handleMenuClick must close the drawer')
  assert.ok(
    menuClick.indexOf('uiStore.closeMobileDrawer()') < menuClick.indexOf("if (path === 'knowledge-bases')"),
    'closeMobileDrawer must run before menu navigation',
  )
  const gotoPage = menu.slice(
    menu.indexOf('const gotopage = async (path: string) => {'),
    menu.indexOf('const gotopage = async (path: string) => {') + 800,
  )
  assert.ok(gotoPage.includes('uiStore.closeMobileDrawer()'), 'gotopage must close the drawer')
  assert.ok(
    gotoPage.indexOf('uiStore.closeMobileDrawer()') < gotoPage.indexOf("if (path === 'logout')"),
    'closeMobileDrawer must run before session/logout handling',
  )

  // F14：客户移动态隐藏宿主项目目录栏，且只命中 customer-shell。
  const projectDir = ruleBlocks(mobileCss, '.project-dir-bar')
    .find((block) => block.body.includes('display: none;'))
  assert.ok(projectDir, 'project-dir-bar hide rule not found')
  assert.match(projectDir.prelude, /\.dialogue-wrap\.customer-shell \.project-dir-bar/)

  // F15：免责声明改为静态占位，去掉固定 40px 预留。
  const creatChatStyle = styleBlock(creatChat)
  const creatChat1023 = mediaBlock(creatChatStyle, 'max-width: 1023px')
  assert.match(
    creatChat1023,
    /\.dialogue-wrap\.customer-shell \.customer-disclaimer \{\s*position: static;/,
  )
  assert.match(
    creatChat1023,
    /padding: 6px max\(12px, var\(--app-safe-left\)\) max\(6px, var\(--app-safe-bottom\)\) max\(12px, var\(--app-safe-right\)\);/,
  )
  assert.doesNotMatch(creatChat1023, /position: absolute/)
  assert.doesNotMatch(creatChatStyle, /padding-bottom: calc\(40px/)
})

// ---------------------------------------------------------------------------
// 8b. CP3 R1 low（F17-F20）：logo 同路径关抽屉、viewport 变量弹层、实例计数
// ---------------------------------------------------------------------------

test('CP3 F17: drawer logo click closes the drawer before routing (same-path safe)', () => {
  const logoClick = menu.slice(
    menu.indexOf('function handleLogoClick()'),
    menu.indexOf('function handleLogoError'),
  )
  assert.ok(logoClick.length > 0, 'handleLogoClick not found')
  assert.match(logoClick, /if \(mobileDrawer\.value\) uiStore\.closeMobileDrawer\(\);/)
  const closeAt = logoClick.indexOf('uiStore.closeMobileDrawer()')
  const pushAt = logoClick.indexOf('router.push(')
  assert.ok(closeAt >= 0 && pushAt > closeAt, 'closeMobileDrawer must run before router.push')
})

test('CP3 F18: viewport-width variable feeds the body-teleported overlays', () => {
  // JS 写入：innerWidth / zoom 的 CSS px。
  assert.match(responsive, /const VIEWPORT_WIDTH_VAR = '--app-viewport-width'/)
  assert.match(
    responsive,
    /root\.style\.setProperty\(VIEWPORT_WIDTH_VAR, `\$\{Math\.round\(viewportWidth\.value \/ zoom\)\}px`\)/,
  )

  // 四个弹层/下拉：宽度改用 var(--app-viewport-width)，不再直接写 100vw/100dvh。
  const targets = [
    ['.t-dialog', 't-dialog'],
    ['.t-popup__content', 't-popup content'],
    ['.language-dropdown', 'language dropdown'],
    ['.user-dropdown', 'user dropdown'],
  ]
  for (const [selector, label] of targets) {
    const block = ruleBlocks(mobileCss, selector).find((item) =>
      item.body.includes('var(--app-viewport-width'),
    )
    assert.ok(block, `${label} must size with var(--app-viewport-width)`)
    assert.doesNotMatch(
      block.body,
      /(?:^|[;{\s])(?:max-width|width|max-height|height)\s*:\s*100(?:vw|dvh|dvw)\b/,
      `${label} must not use a raw viewport unit for sizing`,
    )
  }

  // 用户下拉高度改用已换算的动态视口高度变量。
  const userDropdown = ruleBlocks(mobileCss, '.user-dropdown').find((item) =>
    item.body.includes('max-height'),
  )
  assert.ok(userDropdown, 'user dropdown height rule not found')
  assert.match(userDropdown.body, /max-height: calc\(var\(--app-visual-viewport-height, 100dvh\) - 96px\);/)
})

test('CP3 F19: activeInstances increment/decrement are paired under tracksInstance', () => {
  assert.match(responsive, /const tracksInstance = !!getCurrentScope\(\)/)
  // 自增只在 tracksInstance 时发生，且没有无条件自增残留。
  assert.match(responsive, /if \(tracksInstance\) activeInstances \+= 1/)
  assert.doesNotMatch(responsive, /\n\s*activeInstances \+= 1/)
  // onScopeDispose(cleanup) 只在 tracksInstance 时登记，且只登记一次。
  assert.match(responsive, /if \(tracksInstance\) onScopeDispose\(cleanup\)/)
  assert.equal((responsive.match(/onScopeDispose\(cleanup\)/g) || []).length, 1)
})

// ---------------------------------------------------------------------------
// 9. 组件级守卫（CP3 逻辑轴 L9）：移动类必须由客户条件派生
// ---------------------------------------------------------------------------

const router = read('../../router/index.ts')

test('component-level L9: chat / Input-field mobile state is customer-derived', () => {
  // chat：is-mobile 来自 customerMobile，而 customerMobile 必须同时要求客户 + 移动。
  assert.match(chat, /const customerMobile = computed\(\(\) => isCustomer\.value && isMobileLayout\.value\)/)
  assert.match(chat, /'customer-shell': isCustomer && !embeddedMode,\s*'is-mobile': customerMobile,/)
  const chatStyle = styleBlock(chat)
  const chatMobileSelectors = selectorsIn(chatStyle).filter((selector) => selector.includes('is-mobile'))
  assert.ok(chatMobileSelectors.length >= 5, `expected mobile selectors, got ${chatMobileSelectors.length}`)
  for (const selector of chatMobileSelectors) {
    assert.match(
      selector,
      /^\.chat\.customer-shell\.is-mobile\b/,
      `chat mobile selector must be customer-scoped: ${selector}`,
    )
  }

  // Input-field：is-mobile 来自 isCustomerMobile（customerMode && isMobileLayout）。
  assert.match(inputField, /const isCustomerMobile = computed\(\(\) => props\.customerMode && isMobileLayout\.value\)/)
  assert.match(
    inputField,
    /<div class="answers-input" :class="\{ 'is-embedded': embeddedMode, 'is-compact': compact, 'is-mobile': isCustomerMobile \}"/,
  )
  const inputStyle = styleBlock(inputField)
  const inputMobileAt = inputStyle.indexOf('is-mobile')
  assert.ok(inputMobileAt > 0, 'Input-field mobile style rule not found')
  assert.equal((inputStyle.match(/is-mobile/g) || []).length, 1)
  assert.ok(
    inputMobileAt > inputStyle.indexOf('.answers-input {'),
    'Input-field mobile rule must nest under .answers-input',
  )
  assert.match(inputStyle.slice(inputMobileAt - 2, inputMobileAt + 10), /&\.is-mobile/)
})

test('component-level L9: welcome / creatChat / auth mobile blocks stay page-local', () => {
  // CustomerWelcome 只在客户 creatChat 中渲染，不能出现在管理端页面。
  assert.match(creatChat, /<CustomerWelcome v-if="isCustomer" @send="handleSampleQuestion" \/>/)
  const srcRoot = fileURLToPath(new URL('../../', import.meta.url))
  const welcomeConsumers = walkSourceFiles(srcRoot)
    .filter((file) => !file.endsWith('CustomerWelcome.vue'))
    .filter((file) => readFileSync(file, 'utf8').includes('CustomerWelcome'))
    .map((file) => file.slice(srcRoot.length).replace(/\\/g, '/'))
  assert.deepEqual(welcomeConsumers, ['views/creatChat/creatChat.vue'])

  const welcome1023 = mediaBlock(styleBlock(welcome), 'max-width: 1023px')
  const welcomeSelectors = selectorsIn(welcome1023)
  assert.ok(welcomeSelectors.length >= 6, `expected welcome mobile rules, got ${welcomeSelectors.length}`)
  for (const selector of welcomeSelectors) {
    for (const part of selector.split(',')) {
      assert.match(
        part.trim(),
        /^(\.customer-welcome|\.line-card)/,
        `welcome mobile selector escapes component scope: ${part.trim()}`,
      )
    }
  }

  // creatChat 的 1023 段只作用于 customer-shell。
  const creatChat1023 = mediaBlock(styleBlock(creatChat), 'max-width: 1023px')
  for (const selector of selectorsIn(creatChat1023)) {
    for (const part of selector.split(',')) {
      assert.match(
        part.trim(),
        /^\.dialogue-wrap\.customer-shell\b/,
        `creatChat mobile selector escapes customer shell: ${part.trim()}`,
      )
    }
  }

  // Login 的 1023 段只碰登录页自身类，不泄漏到共享壳。
  const login1023 = mediaBlock(styleBlock(login), 'max-width: 1023px')
  for (const selector of selectorsIn(login1023)) {
    for (const part of selector.split(',')) {
      const trimmed = part.trim()
      assert.match(trimmed, /^(\.login-|\.showcase-|\.form-)/, `login mobile selector escapes auth page: ${trimmed}`)
      assert.doesNotMatch(trimmed, /\.(chat|customer-shell|answers-input|settings-|aside_box|menu_)/)
    }
  }

  // Onboarding 的 560 / 360 段同样只碰引导页自身类（该页没有 1023 段）。
  const onboardingStyle = styleBlock(onboarding)
  for (const query of ['max-width: 560px', 'max-width: 360px']) {
    for (const selector of selectorsIn(mediaBlock(onboardingStyle, query))) {
      for (const part of selector.split(',')) {
        const trimmed = part.trim()
        assert.match(
          trimmed,
          /^(\.workspace-|\.invite-only-notice|h1\b)/,
          `onboarding mobile selector escapes auth page: ${trimmed}`,
        )
        assert.doesNotMatch(trimmed, /\.(chat|customer-shell|answers-input|settings-|aside_box|menu_)/)
      }
    }
  }

  // 两个认证页只挂在登录/引导路由上，不成为管理端页面。
  assert.match(router, /path: "\/login",[\s\S]*?views\/auth\/Login\.vue/)
  assert.match(router, /path: "\/onboarding\/workspace",[\s\S]*?views\/auth\/WorkspaceOnboarding\.vue/)
})
