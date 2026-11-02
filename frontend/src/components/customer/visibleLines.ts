import type { ProductLine } from '@/brand/types'

/**
 * 计算欢迎区首页可见的产线卡片（T02 排序修复）。
 *
 * 规则统一：
 * - 未超过上限：原样返回绑定顺序——点击只改选中高亮，不因选中把卡片换位。
 * - 超过上限：当前选中排第一，其余按原绑定顺序补足上限数量；放不下的收进「查看更多」弹窗。
 *   （弹窗自身仍展示全部产线、当前选中置顶，另由 pickerLines 负责，不经此函数。）
 *
 * @param lines 全部产线（绑定顺序）
 * @param currentId 当前选中产线 id
 * @param maxVisible 可见上限（移动端 2 / 桌面 4，由调用方按布局给定）
 */
export function computeVisibleLines(
  lines: ProductLine[],
  currentId: string | undefined,
  maxVisible: number,
): ProductLine[] {
  // 未超限：保留绑定顺序，避免未超限时也按选中重排。
  if (lines.length <= maxVisible) return lines
  // 超限：当前选中置顶，其余按绑定顺序补足，多的截掉进弹窗。
  const current = lines.find((line) => line.id === currentId)
  if (!current) return lines.slice(0, maxVisible)
  return [current, ...lines.filter((line) => line.id !== currentId)].slice(0, maxVisible)
}
