import { defineStore } from 'pinia'
import { clampSidebarWidth, SIDEBAR_COLLAPSED_WIDTH, SIDEBAR_COLLAPSE_THRESHOLD } from '../utils/sidebarWidth'

export const useUIStore = defineStore('ui', {
  state: () => ({
    showSettingsModal: false,
    showKBEditorModal: false,
    kbEditorMode: 'create' as 'create' | 'edit',
    currentKBId: null as string | null,
    kbEditorType: 'document' as 'document' | 'faq',
    // 当前选中的标签 ID，用于文件上传时传递
    selectedTagIds: [] as string[],
    kbEditorInitialSection: null as string | null,
    settingsInitialSection: null as string | null,
    settingsInitialSubSection: null as string | null,
    manualEditorVisible: false,
    manualEditorMode: 'create' as 'create' | 'edit',
    manualEditorKBId: null as string | null,
    // Soft chat preference: resolved against the writable candidate list.
    manualEditorPreferredKbIds: [] as string[],
    manualEditorKnowledgeId: null as string | null,
    manualEditorInitialTitle: '',
    manualEditorInitialContent: '',
    manualEditorInitialStatus: 'draft' as 'draft' | 'publish',
    manualEditorOnSuccess: null as null | ((payload: { kbId: string; knowledgeId: string; status: 'draft' | 'publish' }) => void),
    sidebarCollapsed: localStorage.getItem('sidebar_collapsed') === 'true',
    sidebarWidth: clampSidebarWidth(Number(localStorage.getItem('sidebar_width'))),
    sidebarResizing: false,
    sidebarBrowserStatus: localStorage.getItem('sidebar_browser_status') !== 'false',
    // 客户移动端的侧栏抽屉开关。只存在于内存，不写 localStorage，
    // 避免污染桌面端管理员的 sidebar_collapsed 偏好。
    mobileDrawerOpen: false,
    // 客户未绑定 agent 的提示弹窗。纯内存：关闭后 store 不变就保持关闭，
    // 下次进入欢迎页或再次点发送时由对应入口重新打开。
    customerNoAgentDialogVisible: false,
  }),

  getters: {
    sidebarDisplayWidth: (state) => state.sidebarCollapsed ? SIDEBAR_COLLAPSED_WIDTH : state.sidebarWidth,
  },

  actions: {
    openSettings(section?: string, subSection?: string) {
      this.settingsInitialSection = section || null
      this.settingsInitialSubSection = subSection || null
      this.showSettingsModal = true
    },

    closeSettings() {
      this.showSettingsModal = false
      this.settingsInitialSection = null
      this.settingsInitialSubSection = null
    },

    toggleSettings() {
      this.showSettingsModal = !this.showSettingsModal
    },

    openKBSettings(kbId: string, initialSection?: string) {
      this.currentKBId = kbId
      this.kbEditorMode = 'edit'
       this.kbEditorType = 'document'
      this.kbEditorInitialSection = initialSection || null
      this.showKBEditorModal = true
    },

    openEditKB(kbId: string, initialSection?: string) {
      this.openKBSettings(kbId, initialSection)
    },

    openCreateKB(type: 'document' | 'faq' = 'document', initialSection?: string) {
      this.currentKBId = null
      this.kbEditorMode = 'create'
      this.kbEditorType = type
      this.kbEditorInitialSection = initialSection || null
      this.showKBEditorModal = true
    },

    closeKBEditor() {
      this.showKBEditorModal = false
      this.currentKBId = null
      this.kbEditorInitialSection = null
      this.kbEditorType = 'document'
    },

    openManualEditor(options: {
      mode?: 'create' | 'edit'
      kbId?: string | null
      /** Soft preference from chat citations; resolved against writable candidates. */
      preferredKbIds?: string[] | null
      knowledgeId?: string | null
      title?: string
      content?: string
      status?: 'draft' | 'publish'
      onSuccess?: (payload: { kbId: string; knowledgeId: string; status: 'draft' | 'publish' }) => void
    } = {}) {
      this.manualEditorMode = options.mode || 'create'
      this.manualEditorKBId = options.kbId ?? null
      this.manualEditorPreferredKbIds = (options.preferredKbIds || []).filter(
        (id): id is string => typeof id === 'string' && !!id.trim(),
      )
      this.manualEditorKnowledgeId = options.knowledgeId ?? null
      this.manualEditorInitialTitle = options.title || ''
      this.manualEditorInitialContent = options.content || ''
      this.manualEditorInitialStatus = options.status || 'draft'
      this.manualEditorOnSuccess = options.onSuccess || null
      this.manualEditorVisible = true
    },

    closeManualEditor() {
      this.manualEditorVisible = false
      this.manualEditorKBId = null
      this.manualEditorPreferredKbIds = []
      this.manualEditorKnowledgeId = null
      this.manualEditorInitialContent = ''
      this.manualEditorInitialTitle = ''
      this.manualEditorInitialStatus = 'draft'
      this.manualEditorOnSuccess = null
    },

    notifyManualEditorSuccess(payload: { kbId: string; knowledgeId: string; status: 'draft' | 'publish' }) {
      if (typeof this.manualEditorOnSuccess === 'function') {
        try {
          this.manualEditorOnSuccess(payload)
        } catch (err) {
          console.error('Manual editor success callback error:', err)
        }
      }
      this.manualEditorOnSuccess = null
    },

    // 设置当前选中的标签 ID
    toggleSelectedTagId(tagId: string) {
      const idx = this.selectedTagIds.indexOf(tagId)
      if (idx >= 0) {
        this.selectedTagIds.splice(idx, 1)
      } else {
        this.selectedTagIds.push(tagId)
      }
    },

    clearSelectedTagIds() {
      this.selectedTagIds = []
    },

    setSidebarBrowserStatus(visible: boolean) {
      this.sidebarBrowserStatus = visible
      localStorage.setItem('sidebar_browser_status', String(visible))
    },

    // persist:false 用于客户版：只改当前界面的内存态，不覆盖同一浏览器里
    // 管理员保存的 sidebar_collapsed 偏好（C6 / M4）。
    toggleSidebar(options: { persist?: boolean } = {}) {
      this.sidebarCollapsed = !this.sidebarCollapsed
      if (options.persist !== false) {
        localStorage.setItem('sidebar_collapsed', String(this.sidebarCollapsed))
      }
    },

    resizeSidebar(width: number, options: { persist?: boolean } = {}) {
      if (!Number.isFinite(width)) return
      if (width < SIDEBAR_COLLAPSE_THRESHOLD) {
        this.collapseSidebar(options)
        return
      }
      this.sidebarWidth = clampSidebarWidth(width)
      if (options.persist !== false) {
        localStorage.setItem('sidebar_width', String(this.sidebarWidth))
      }
      this.expandSidebar(options)
    },

    collapseSidebar(options: { persist?: boolean } = {}) {
      this.sidebarCollapsed = true
      if (options.persist !== false) {
        localStorage.setItem('sidebar_collapsed', 'true')
      }
    },

    expandSidebar(options: { persist?: boolean } = {}) {
      this.sidebarCollapsed = false
      if (options.persist !== false) {
        localStorage.setItem('sidebar_collapsed', 'false')
      }
    },

    // 移动端抽屉：纯内存状态，不做持久化。
    openMobileDrawer() {
      this.mobileDrawerOpen = true
    },

    closeMobileDrawer() {
      this.mobileDrawerOpen = false
    },

    toggleMobileDrawer() {
      this.mobileDrawerOpen = !this.mobileDrawerOpen
    },

    // 未绑定 agent 弹窗：只控制界面提示，关闭仅影响当前这一次（W6）。
    openCustomerNoAgentDialog() {
      this.customerNoAgentDialogVisible = true
    },

    closeCustomerNoAgentDialog() {
      this.customerNoAgentDialogVisible = false
    },
  }
})
