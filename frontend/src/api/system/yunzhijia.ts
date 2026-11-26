import { get, post } from '@/utils/request'

/**
 * 云之家同步管理端接口（T08 后端，T10 前端消费）。
 *
 * 权限由后端逐接口校验（C4）；本模块不做角色判断。
 * 预检与手动同步要跑完整轮，超时放宽到 120 秒，避免浏览器先断而
 * 后台仍在同步。响应均不含手机号、ticket、token。
 */

const BASE = '/api/v1/system/admin/yunzhijia'

/** 预检/手动同步的前端等待上限；与后端单轮耗时对齐。 */
export const YUNZHIJIA_SYNC_TIMEOUT_MS = 120_000

export interface YunzhijiaSyncAnomaly {
  kind: string
  open_id?: string
  name?: string
  user_id?: string
  detail?: string
}

/** 最近一次真实同步运行的摘要（后端 latest）。 */
export interface YunzhijiaRunSummary {
  id: number
  trigger: string
  status: string
  started_at: string
  finished_at?: string
  duration_ms: number
  active_count: number
  inactive_count: number
  created_count: number
  /** 本轮核对过的已有在职账号（含无变化），界面文案为「已核对」。 */
  updated_count: number
  disabled_count: number
  migrated_count: number
  anomaly_count: number
  anomalies: YunzhijiaSyncAnomaly[]
  error_summary?: string
}

export interface YunzhijiaBlockedEmployee {
  open_id: string
  name: string
  department: string
  blocked_at?: string
  blocked_by?: string
}

export interface YunzhijiaAdminStatus {
  enabled: boolean
  latest?: YunzhijiaRunSummary
  blocked: YunzhijiaBlockedEmployee[]
}

export interface YunzhijiaPreviewResult {
  active_count: number
  inactive_count: number
  tenant_default_count: number
  tenant_hr_count: number
  anomalies: YunzhijiaSyncAnomaly[]
}

/** 手动同步接口直接返回运行记录（不含 duration_ms，展示以随后刷新为准）。 */
export interface YunzhijiaSyncRun {
  id: number
  trigger: string
  status: string
  started_at: string
  finished_at?: string
  active_count: number
  inactive_count: number
  created_count: number
  updated_count: number
  disabled_count: number
  migrated_count: number
  anomaly_count: number
  error_summary?: string
}

export interface YunzhijiaUnblockResponse {
  success: boolean
  message?: string
}

/** GET /system/admin/yunzhijia/sync：enabled、最近运行与 blocked 清单。 */
export async function getYunzhijiaSyncStatus(): Promise<YunzhijiaAdminStatus> {
  return (await get(`${BASE}/sync`)) as unknown as YunzhijiaAdminStatus
}

/** POST /system/admin/yunzhijia/sync/preview：只读预检，不写账号。 */
export async function previewYunzhijiaSync(): Promise<YunzhijiaPreviewResult> {
  return (await post(`${BASE}/sync/preview`, {}, {
    timeout: YUNZHIJIA_SYNC_TIMEOUT_MS,
  })) as unknown as YunzhijiaPreviewResult
}

/** POST /system/admin/yunzhijia/sync：手动同步；运行中后端返回 409。 */
export async function runYunzhijiaSync(): Promise<YunzhijiaSyncRun> {
  return (await post(`${BASE}/sync`, {}, {
    timeout: YUNZHIJIA_SYNC_TIMEOUT_MS,
  })) as unknown as YunzhijiaSyncRun
}

/** POST /system/admin/yunzhijia/employees/:open_id/unblock：解除禁止重建。 */
export async function unblockYunzhijiaEmployee(
  openId: string,
): Promise<YunzhijiaUnblockResponse> {
  return (await post(
    `${BASE}/employees/${encodeURIComponent(openId)}/unblock`,
    {},
  )) as unknown as YunzhijiaUnblockResponse
}
