import { getCurrentScope, onScopeDispose, ref, type Ref } from 'vue'
import { getRootZoom } from '@/utils/zoom'

/**
 * 客户版移动布局的唯一断点：宽度小于该值（CSS px）走移动布局。
 * 其他组件不要再写 1024 这类魔法数字，统一从这里引用。
 */
export const MOBILE_LAYOUT_MAX_WIDTH = 1024

/** useFont 在根字号真正变化后派发的事件名。 */
const FONT_SCALE_EVENT = 'weknora:font-scale-change'

/** 客户移动态写到 <html> 上的动态视口高度变量。 */
const VIEWPORT_HEIGHT_VAR = '--app-visual-viewport-height'

/** 把无效 zoom（0、NaN、负数）按 1 处理，避免除零和媒体查询写出非法值。 */
function normalizeZoom(zoom: number): number {
  return Number.isFinite(zoom) && zoom > 0 ? zoom : 1
}

/**
 * 生成按根字号缩放换算后的媒体查询。
 * 例：baseline=1024、zoom=1.125 时返回 `(max-width: 1152px)`。
 */
export function scaledMaxWidthQuery(baselinePx: number, zoom: number): string {
  return `(max-width: ${baselinePx * normalizeZoom(zoom)}px)`
}

/**
 * 纯函数，便于单测：把视觉宽度除以 zoom 换成 CSS px，再和断点比较。
 */
export function resolveIsMobileLayout(width: number, zoom: number): boolean {
  return width / normalizeZoom(zoom) < MOBILE_LAYOUT_MAX_WIDTH
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
 * - isMobileLayout：是否小于 1024 CSS px（已按根字号 zoom 换算）；
 * - viewportWidth / viewportHeight：CSS px，键盘弹出时高度会跟着 visualViewport 变；
 * - isCoarsePointer：触屏设备（可选判断，不参与布局）。
 *
 * 客户移动态会把实时高度写到 <html> 的 --app-visual-viewport-height，
 * 管理员态一律移除，避免管理端样式受到影响。
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

  const root = document.documentElement
  const visualViewport = window.visualViewport ?? null
  let widthQuery: MediaQueryList | null = null
  let coarseQuery: MediaQueryList | null = null

  /** 客户移动态写高度变量；其他情况（含管理端）移除。 */
  function syncViewportHeightVar() {
    if (!root) return
    const isCustomer = root.dataset.customer === 'true'
    if (isMobileLayout.value && isCustomer && viewportHeight.value > 0) {
      root.style.setProperty(VIEWPORT_HEIGHT_VAR, `${Math.round(viewportHeight.value)}px`)
    } else {
      root.style.removeProperty(VIEWPORT_HEIGHT_VAR)
    }
  }

  function measure() {
    const zoom = normalizeZoom(getRootZoom())
    viewportWidth.value = window.innerWidth / zoom
    const rawHeight =
      visualViewport && visualViewport.height > 0 ? visualViewport.height : window.innerHeight
    viewportHeight.value = rawHeight / zoom
    // 有 matchMedia 时以它为准；极端环境下退回纯函数计算。
    isMobileLayout.value = widthQuery
      ? widthQuery.matches
      : resolveIsMobileLayout(window.innerWidth, zoom)
    syncViewportHeightVar()
  }

  function onViewportChange() {
    measure()
  }

  /** 根字号变了，媒体查询里的缩放宽度也要重建。 */
  function rebuildWidthQuery() {
    if (widthQuery) {
      widthQuery.removeEventListener('change', onViewportChange)
      widthQuery = null
    }
    const zoom = normalizeZoom(getRootZoom())
    if (typeof window.matchMedia === 'function') {
      widthQuery = window.matchMedia(scaledMaxWidthQuery(MOBILE_LAYOUT_MAX_WIDTH, zoom))
      widthQuery.addEventListener('change', onViewportChange)
    }
    measure()
  }

  rebuildWidthQuery()

  if (typeof window.matchMedia === 'function') {
    coarseQuery = window.matchMedia('(pointer: coarse)')
    isCoarsePointer.value = coarseQuery.matches
    coarseQuery.addEventListener('change', onViewportChange)
  }

  // data-customer 可能在组件挂载后才切换（登录/退出），变了要重新决定是否写高度变量。
  const customerObserver = new MutationObserver(syncViewportHeightVar)
  customerObserver.observe(root, { attributes: true, attributeFilter: ['data-customer'] })

  window.addEventListener('resize', onViewportChange)
  window.addEventListener(FONT_SCALE_EVENT, rebuildWidthQuery)
  visualViewport?.addEventListener('resize', onViewportChange)
  visualViewport?.addEventListener('scroll', onViewportChange)

  function cleanup() {
    window.removeEventListener('resize', onViewportChange)
    window.removeEventListener(FONT_SCALE_EVENT, rebuildWidthQuery)
    visualViewport?.removeEventListener('resize', onViewportChange)
    visualViewport?.removeEventListener('scroll', onViewportChange)
    if (widthQuery) {
      widthQuery.removeEventListener('change', onViewportChange)
      widthQuery = null
    }
    if (coarseQuery) {
      coarseQuery.removeEventListener('change', onViewportChange)
      coarseQuery = null
    }
    customerObserver.disconnect()
  }

  // 只有确实处在组件/effect 作用域里时才登记清理，避免测试里单独调用时报错。
  if (getCurrentScope()) onScopeDispose(cleanup)

  return { isMobileLayout, viewportWidth, viewportHeight, isCoarsePointer }
}
