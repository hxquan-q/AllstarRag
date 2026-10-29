import { nextTick, onScopeDispose, watch, type Ref } from 'vue'

/**
 * 移动抽屉的焦点陷阱。
 *
 * - 打开时记住当前焦点，并把焦点移进容器（第一个可聚焦元素，找不到就聚焦容器本身）。
 * - Tab / Shift+Tab 在容器内循环，不会跑到背景页面。
 * - 关闭时把焦点还给调用方指定的位置（默认回到打开前的元素）。
 *
 * 背景区域的 inert / aria-hidden 由 platform 统一加，本文件不处理。
 */

const FOCUSABLE_SELECTOR = [
    'a[href]',
    'button:not([disabled])',
    'input:not([disabled])',
    'select:not([disabled])',
    'textarea:not([disabled])',
    '[tabindex]:not([tabindex="-1"])',
].join(',')

export interface UseFocusTrapOptions {
    /** 关闭后希望焦点回到的元素；不传则回到打开前的 activeElement。 */
    restoreFocus?: () => HTMLElement | null
}

/**
 * @param container 抽屉根节点（需要能拿到 DOM）
 * @param active 是否处于打开态
 */
export function useFocusTrap(
    container: Ref<HTMLElement | null>,
    active: Ref<boolean>,
    options: UseFocusTrapOptions = {},
): void {
    let lastFocused: HTMLElement | null = null
    let isActive = false

    function listFocusable(): HTMLElement[] {
        const root = container.value
        if (!root) return []
        return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
            .filter((el) => !el.hasAttribute('disabled') && el.getClientRects().length > 0)
    }

    function onKeydown(event: KeyboardEvent) {
        if (!isActive || event.key !== 'Tab') return
        const root = container.value
        if (!root) return
        const items = listFocusable()
        if (items.length === 0) {
            event.preventDefault()
            root.focus()
            return
        }
        const first = items[0]
        const last = items[items.length - 1]
        if (!first || !last) {
            event.preventDefault()
            root.focus()
            return
        }
        const current = document.activeElement as HTMLElement | null
        if (event.shiftKey) {
            if (current === first || !current || !root.contains(current)) {
                event.preventDefault()
                last.focus()
            }
            return
        }
        if (current === last || !current || !root.contains(current)) {
            event.preventDefault()
            first.focus()
        }
    }

    async function activate() {
        if (typeof document === 'undefined') return
        isActive = true
        lastFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null
        await nextTick()
        if (!active.value) return
        const root = container.value
        if (!root) return
        const items = listFocusable()
        const target = items[0] ?? root
        target.focus()
    }

    function deactivate() {
        if (!isActive) return
        isActive = false
        if (typeof document === 'undefined') return
        const target = options.restoreFocus?.() ?? lastFocused
        lastFocused = null
        if (target && typeof target.focus === 'function') {
            // 等渲染更新后再还焦点，避免目标还处于 inert 状态时聚焦失败；
            // 若期间抽屉又被打开，就不抢回焦点。
            nextTick(() => {
                if (!isActive) target.focus()
            })
        }
    }

    if (typeof document !== 'undefined') {
        document.addEventListener('keydown', onKeydown, true)
    }

    watch(active, (open) => {
        if (open) void activate()
        else deactivate()
    }, { immediate: true, flush: 'post' })

    if (typeof document !== 'undefined') {
        onScopeDispose(() => {
            document.removeEventListener('keydown', onKeydown, true)
            // 卸载时复位内部状态，避免复用实例/热更新时残留打开标记与旧焦点（CP3 F10）。
            isActive = false
            lastFocused = null
        })
    }
}
