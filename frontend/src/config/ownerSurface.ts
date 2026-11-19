/**
 * T03（admin-feature-surface）owner 端功能面裁剪表：发布集成分区。
 *
 * 这些表只决定「界面上是否渲染入口、deep-link 是否落到 role-denied 兜底」，
 * 不做权限判定（UI gating only）：chrome / cli / claw 都是纯外链页，没有
 * 后端接口；同一裁定清单里 Ollama / 小爱云分区的真权限在后端
 * RequireSystemAdmin（T01 已对空间角色 403），见 C4。
 *
 * 纯数据模块、无 Vue / Pinia 依赖（照抄 customerAccess.ts 的模式）。
 * 消费方是 Settings.vue 的 canSeeSection 集成分区分支；同一裁定清单的
 * Ollama / 小爱云（ollama / weknoracloud）分区不是集成分区，闸门直接
 * 内联在 canSeeSection 里（表驱动与内联的拆分原因见 T03-notes）。
 */

/** 全角色删除的发布集成 tab（claw）：导航不渲染，deep-link 兜底不可见。 */
export const REMOVED_INTEGRATION_TABS: ReadonlySet<string> = new Set(['claw'])

/**
 * 仅系统管理员可见的发布集成 tab（chrome / cli）。
 * 超管保留是用户有意为之，勿与 owner 端统一（ADJUDICATIONS 2026-11-18）。
 */
export const SYSTEM_ADMIN_ONLY_INTEGRATION_TABS: ReadonlySet<string> = new Set(['chrome', 'cli'])
