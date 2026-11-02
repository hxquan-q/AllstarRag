import { getCurrentScope, onScopeDispose, ref, type Ref } from 'vue'
import { getRootZoom } from '@/utils/zoom'

/**
 * 客户版移动布局的唯一断点：宽度小于该值（CSS px）走移动布局。
 * 其他组件不要再写 1024 这类魔法数字，统一从这里引用。
 */
export const MOBILE_LAYOUT_MAX_WIDTH = 1024

/**
 * 与 customer-mobile.css 的 @media (max-width: 1023px) 完全同口径。
 * 不能用 zoom 换算查询：Chrome 里 html { zoom } 不改变 window.innerWidth，
 * 也不改变媒体查询结果；再乘 zoom 会让 JS 与 CSS 判断分叉。
 */
export const MOBILE_LAYOUT_QUERY = '(max-width: 1023px)'

/** useFont 在根字号真正变化后派发的事件名。 */
const FONT_SCALE_EVENT = 'weknora:font-scale-change'

/** 客户移动态写到 <html> 上的动态视口高度变量。 */
const VIEWPORT_HEIGHT_VAR = '--app-visual-viewport-height'

/**
 * 客户移动态写到 <html> 上的视口宽度变量（CP3 F18）。
 * window.innerWidth 是视觉像素，写成 CSS 长度前要除以 root zoom；
 * zoom 渲染时再乘回来，弹层拿到的就是真实视口宽。
 */
const VIEWPORT_WIDTH_VAR = '--app-viewport-width'

/** 当前存活的 hook 实例数；归零时把写进 <html> 的高度变量清掉（CP3 F10）。 */
let activeInstances = 0

/** 把无效 zoom（0、NaN、负数）按 1 处理，避免除零。 */
function normalizeZoom(zoom: number): number {
  return Number.isFinite(zoom) && zoom > 0 ? zoom : 1
}

/**
 * 纯函数，便于单测：与媒体查询同口径，直接按视口宽度比较。
 * zoom 只影响渲染尺寸，不参与断点判定。
 */
export function resolveIsMobileLayout(width: number): boolean {
  return width < MOBILE_LAYOUT_MAX_WIDTH
}

export interface ResponsiveLayoutState {
  isMobileLayout: Ref<boolean>
  viewportWidth: Ref<number>
  viewportHeight: Ref<number>
  isCoarsePointer: Ref<boolean>
}

/**
 * 客户版移动布局/视口的唯一入口。
 *
 * - isMobileLayout：以 matchMedia(MOBILE_LAYOUT_QUERY) 为准，与 CSS 媒体查询一致；
 * - viewportWidth：window.innerWidth，与媒体查询同一口径；
 * - viewportHeight：visualViewport 高度按根字号 zoom 换算成 CSS px，键盘弹出时跟着变；
 * - isCoarsePointer：触屏设备（可选判断，不参与布局）。
 *
 * 客户移动态会把实时视口宽高写到 <html> 的 --app-viewport-width /
 * --app-visual-viewport-height，管理员态一律移除，避免管理端样式受到影响。
 */
export function useResponsiveLayout(): ResponsiveLayoutState {
  const isMobileLayout = ref(false)
  const viewportWidth = ref(1440)
  const viewportHeight = ref(900)
  const isCoarsePointer = ref(false)

  // SSR / 单测环境没有 window：返回安全默认值，不注册监听。
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    return { isMobileLayout, viewportWidth, viewportHeight, isCoarsePointer }
  }

  // 无 scope 的单测调用不登记清理，因此也不计入实例数；自增与减量必须成对（CP3 F19）。
  const tracksInstance = !!getCurrentScope()
  if (tracksInstance) activeInstances += 1

  const root = document.documentElement
  const visualViewport = window.visualViewport ?? null
  let widthQuery: MediaQueryList | null = null
  let coarseQuery: MediaQueryList | null = null

  /** 客户移动态写视口变量；其他情况（含管理端）移除。 */
  function syncViewportVars() {
    if (!root) return
    const isCustomer = root.dataset.customer === 'true'
    if (isMobileLayout.value && isCustomer) {
      if (viewportWidth.value > 0) {
        // 视觉像素 → CSS px：除以 zoom，避免弹层在放大字号时溢出视口。
        const zoom = normalizeZoom(getRootZoom())
        root.style.setProperty(VIEWPORT_WIDTH_VAR, `${Math.round(viewportWidth.value / zoom)}px`)
      } else {
        root.style.removeProperty(VIEWPORT_WIDTH_VAR)
      }
      if (viewportHeight.value > 0) {
        root.style.setProperty(VIEWPORT_HEIGHT_VAR, `${Math.round(viewportHeight.value)}px`)
      } else {
        root.style.removeProperty(VIEWPORT_HEIGHT_VAR)
      }
    } else {
      root.style.removeProperty(VIEWPORT_WIDTH_VAR)
      root.style.removeProperty(VIEWPORT_HEIGHT_VAR)
    }
  }

  function measure() {
    // 宽度与媒体查询同口径，不除以 zoom。
    viewportWidth.value = window.innerWidth
    const zoom = normalizeZoom(getRootZoom())
    // 高度是 CSS 长度，需要除以 zoom 才是实际的 CSS px。
    const rawHeight =
      visualViewport && visualViewport.height > 0 ? visualViewport.height : window.innerHeight
    viewportHeight.value = rawHeight / zoom
    // 有 matchMedia 时以它为准；极端环境下退回纯函数计算。
    isMobileLayout.value = widthQuery
      ? widthQuery.matches
      : resolveIsMobileLayout(window.innerWidth)
    syncViewportVars()
  }

  function onViewportChange() {
    measure()
  }

  function onCoarsePointerChange(event: MediaQueryListEvent) {
    isCoarsePointer.value = event.matches
  }

  if (typeof window.matchMedia === 'function') {
    widthQuery = window.matchMedia(MOBILE_LAYOUT_QUERY)
    widthQuery.addEventListener('change', onViewportChange)
  }
  measure()

  if (typeof window.matchMedia === 'function') {
    coarseQuery = window.matchMedia('(pointer: coarse)')
    isCoarsePointer.value = coarseQuery.matches
    // 触屏/鼠标切换时同步标志（当前无消费者，但状态必须跟着设备变，CP3 F9）。
    coarseQuery.addEventListener('change', onCoarsePointerChange)
  }

  // data-customer 可能在组件挂载后才切换（登录/退出），变了要重新决定是否写视口变量。
  const customerObserver = new MutationObserver(syncViewportVars)
  customerObserver.observe(root, { attributes: true, attributeFilter: ['data-customer'] })

  window.addEventListener('resize', onViewportChange)
  // 根字号变化只影响高度换算，断点查询不重建（见 MOBILE_LAYOUT_QUERY 注释）。
  window.addEventListener(FONT_SCALE_EVENT, onViewportChange)
  visualViewport?.addEventListener('resize', onViewportChange)
  visualViewport?.addEventListener('scroll', onViewportChange)

  function cleanup() {
    window.removeEventListener('resize', onViewportChange)
    window.removeEventListener(FONT_SCALE_EVENT, onViewportChange)
    visualViewport?.removeEventListener('resize', onViewportChange)
    visualViewport?.removeEventListener('scroll', onViewportChange)
    if (widthQuery) {
      widthQuery.removeEventListener('change', onViewportChange)
      widthQuery = null
    }
    if (coarseQuery) {
      coarseQuery.removeEventListener('change', onCoarsePointerChange)
      coarseQuery = null
    }
    customerObserver.disconnect()
    // 最后一个实例卸载后清掉视口变量，避免残留值影响后续页面（CP3 F10/F18）。
    // 无 scope 的调用没有登记清理，也不参与计数，保证自增与减量成对（CP3 F19）。
    if (tracksInstance) {
      activeInstances = Math.max(0, activeInstances - 1)
      if (activeInstances === 0) {
        root.style.removeProperty(VIEWPORT_WIDTH_VAR)
        root.style.removeProperty(VIEWPORT_HEIGHT_VAR)
      }
    }
  }

  // 只有确实处在组件/effect 作用域里时才登记清理，避免测试里单独调用时报错。
  if (tracksInstance) onScopeDispose(cleanup)

  return { isMobileLayout, viewportWidth, viewportHeight, isCoarsePointer }
}
