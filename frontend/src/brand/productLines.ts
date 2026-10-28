import type { ProductLine } from './types'

/**
 * 产线的唯一数据来源是 `stores/productLine.ts` 里从「当前工作区绑定 agent」派生出的列表
 * （id/name/description/samples 均取 agent 自身），这里只提供与界面无关的纯函数，
 * 方便测试阶段直接跑。
 */

/** 产线偏好在 localStorage 里的键前缀，后面拼用户 id，保证换账号不串。 */
export const PRODUCT_LINE_STORAGE_PREFIX = 'weknora_customer_line:'

/** 生成某个用户的产线持久化键。用户未登录（空 id）时只用于展示，不写入存储。 */
export function productLineStorageKey(userId: string): string {
  return `${PRODUCT_LINE_STORAGE_PREFIX}${userId}`
}

/**
 * 从产线列表里解析出当前应选中的一条。
 *
 * - `storedId` 命中时返回对应产线；
 * - 未传、为空或命中不到（agent 被解绑、停用或删除，手改 localStorage）时回退第一条；
 * - 列表为空时返回 `null`，由调用方决定「无产线可用」的界面。
 *
 * 不修改入参与存储，存储回写由 store 负责。
 */
export function resolveLine(
  lines: readonly ProductLine[] | null | undefined,
  storedId?: string | null,
): ProductLine | null {
  if (!Array.isArray(lines) || lines.length === 0) return null

  if (storedId) {
    const matched = lines.find((line) => line?.id === storedId)
    if (matched) return matched
  }

  return lines[0] ?? null
}
