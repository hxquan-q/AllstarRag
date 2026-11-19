// 失败文档重试限次（T06）：后端额度耗尽时返回 HTTP 429，错误体
// { success:false, error:{ code:1006, message, details:{ error_code, recover_at } } }。
// request 层把响应体摊平后 reject，故 details 实际读在 error.error.details。
// 前端只在本次响应上出人话卡片，不缓存耗尽态——24h 窗口滚动后后端放行，
// 下一次提交自然回到正常重试流程。

/** 后端 details.error_code 里的机器码（internal/application/service/reparse_quota.go）。 */
export const RETRY_LIMIT_EXHAUSTED = 'RETRY_LIMIT_EXHAUSTED'

export interface ReparseQuotaNotice {
  /** 额度恢复时间（后端 RFC3339 原文；老响应缺字段时不显示时间行）。 */
  recoverAt?: string
}

/** 批量重解析响应 data.skipped 里被额度剔除的一条。 */
export interface ReparseQuotaSkip {
  knowledgeId: string
  reason: string
  recoverAt?: string
}

function asRecord(value: unknown): Record<string, any> | null {
  return value !== null && typeof value === 'object' ? (value as Record<string, any>) : null
}

/**
 * 识别 429 RETRY_LIMIT_EXHAUSTED。
 * 兼容两种入参：request 层 reject 的摊平对象（error.details.error_code），
 * 以及直接拿到后端信封 { success:false, error:{...} } 的场景。
 */
export function detectReparseQuotaExhausted(error: unknown): ReparseQuotaNotice | null {
  const err = asRecord(error)
  const payload = asRecord(err?.error) ?? err
  const details = asRecord(payload?.details)
  if (details?.error_code !== RETRY_LIMIT_EXHAUSTED) return null
  return {
    recoverAt: typeof details.recover_at === 'string' ? details.recover_at : undefined,
  }
}

/** 从批量重解析的 200 响应里取出被额度剔除的文档（只认额度耗尽这一种原因）。 */
export function readReparseQuotaSkips(response: unknown): ReparseQuotaSkip[] {
  const data = asRecord(asRecord(response)?.data)
  const skipped = Array.isArray(data?.skipped) ? data.skipped : []
  const result: ReparseQuotaSkip[] = []
  for (const entry of skipped) {
    const item = asRecord(entry)
    if (!item || typeof item.knowledge_id !== 'string') continue
    if (item.reason !== RETRY_LIMIT_EXHAUSTED) continue
    result.push({
      knowledgeId: item.knowledge_id,
      reason: item.reason,
      recoverAt: typeof item.recover_at === 'string' ? item.recover_at : undefined,
    })
  }
  return result
}

/** 把后端 RFC3339 时间格式化成当前语言的本地时间；无效值不显示。 */
export function formatRecoverAt(value?: string, locale?: string): string {
  if (!value) return ''
  const at = new Date(value)
  if (Number.isNaN(at.getTime())) return ''
  return at.toLocaleString(locale || undefined, { dateStyle: 'medium', timeStyle: 'short' })
}
