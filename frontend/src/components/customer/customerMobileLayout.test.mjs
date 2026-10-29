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

  // 站内入口（showSettingsModal=true）默认列表；外部直达 ?section=x 默认内容。
  assert.match(
    settings,
    /const directToSection = route\.path === '\/platform\/settings'[\s\S]*?!uiStore\.showSettingsModal[\s\S]*?typeof route\.query\.section === 'string'[\s\S]*?route\.query\.section !== ''/,
  )
  assert.match(settings, /mobileShowNav\.value = !directToSection/)
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
  assert.match(references, /width: fillViewport\.value \? '100vw' : `\$\{panelWidth\.value\}px`/)

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
