import { defineStore } from 'pinia'
import { ref } from 'vue'
import { get } from '@/utils/request'
import { useAuthStore } from '@/stores/auth'

/**
 * 用户管理能力缓存。
 *
 * 能力字符串与后端 internal/types/capabilities.go 一一对应（AC-12b）：
 * 后端是唯一事实源，这里只缓存 GET /auth/capabilities 的投影，
 * 前端不再另写 role → 权限判断。can() 仅用于界面门控，
 * 真正的权限由后端逐接口校验（C4）。
 */
export type Capability =
  | 'user.view'
  | 'user.invite'
  | 'user.create'
  | 'user.role.change'
  | 'user.ban'
  | 'user.unban'
  | 'user.purge'
  | 'user.owner.add'
  | 'user.owner.remove'
  | 'space.transfer'
  | 'owner.provision'

export interface CapabilitiesPayload {
  role: string
  is_system_admin: boolean
  capabilities: string[]
}

interface CapabilitiesResponse {
  success: boolean
  data?: CapabilitiesPayload
  message?: string
}

export const useCapabilitiesStore = defineStore('capabilities', () => {
  const capabilities = ref<Set<string>>(new Set())
  const role = ref('')
  const isSystemAdmin = ref(false)
  const loaded = ref(false)
  const loading = ref(false)

  // 同一时刻只保留一次请求；登出或切换空间后迟到的响应靠 revision 丢弃，
  // 不能把上一个会话/空间的能力写回当前上下文。
  let inflight: Promise<void> | null = null
  let revision = 0
  // 当前能力所属的租户键；为空表示还没有租户上下文。
  let tenantKey = ''

  /** 解析本次请求所属的租户键；能力投影必须按空间隔离。 */
  const resolveTenantKey = (): string => {
    const tenantId = useAuthStore().effectiveTenantId
    return tenantId == null ? '' : String(tenantId)
  }

  /**
   * 切换空间时作废旧请求并清空旧能力：
   * 否则切空间后的短窗口会沿用上一空间的 role/capabilities 投影。
   */
  const switchTenant = (nextKey: string) => {
    if (nextKey === tenantKey) return
    tenantKey = nextKey
    revision++
    inflight = null
    capabilities.value = new Set()
    role.value = ''
    isSystemAdmin.value = false
    loaded.value = false
    loading.value = false
  }

  const apply = (payload: CapabilitiesPayload) => {
    role.value = payload.role || ''
    isSystemAdmin.value = payload.is_system_admin === true
    capabilities.value = new Set(payload.capabilities ?? [])
    loaded.value = true
  }

  const fetchAndApply = async (requestRevision: number) => {
    loading.value = true
    try {
      const resp = (await get('/api/v1/auth/capabilities')) as unknown as CapabilitiesResponse
      if (requestRevision !== revision) return
      if (resp.success && resp.data) {
        apply(resp.data)
      }
    } catch {
      // 能力是界面门控用的辅助数据：拉取失败保留上一次的值，
      // 不清空已加载能力，也不打断登录流程。
    } finally {
      if (requestRevision === revision) loading.value = false
    }
  }

  /** 拉取并缓存当前空间的能力；失败时保留上一次的值。 */
  const refresh = (): Promise<void> => {
    switchTenant(resolveTenantKey())
    if (inflight) return inflight
    const requestRevision = revision
    const task = fetchAndApply(requestRevision)
    inflight = task
    void task.finally(() => {
      if (inflight === task) inflight = null
    })
    return task
  }

  /** 当前用户是否拥有某项能力（仅用于界面门控）。 */
  const can = (capability: Capability | string): boolean => {
    return capabilities.value.has(capability)
  }

  /** 登出或会话失效时清空缓存。 */
  const reset = () => {
    revision++
    inflight = null
    tenantKey = ''
    capabilities.value = new Set()
    role.value = ''
    isSystemAdmin.value = false
    loaded.value = false
    loading.value = false
  }

  return {
    capabilities,
    role,
    isSystemAdmin,
    loaded,
    loading,
    refresh,
    can,
    reset,
  }
})
