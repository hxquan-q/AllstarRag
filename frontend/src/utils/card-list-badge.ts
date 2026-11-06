/**
 * Helpers to suppress card corner badges that repeat what a visible section
 * header (Group) already communicates.
 */

export type ListCardSectionKey =
  | 'pinned'
  | 'mine'
  | 'tenantOthers'
  | 'builtin'
  | 'sharedByMe'
  | 'sharedEditable'
  | 'sharedReadonly'
  | 'created'
  | 'joined'

export type ResourceOriginVariant = 'mine' | 'tenant' | 'creator' | 'space' | 'shared'

/** ResourceOriginBadge on KB / Agent cards. */
export function shouldShowResourceOriginBadge(opts: {
  section: ListCardSectionKey | null
  variant: ResourceOriginVariant
  creatorName?: string
  /** 创建者已墓碑化时必须展示「已注销用户」，不能因缺名字被抑制。 */
  creatorDeleted?: boolean
  showSectionHeaders?: boolean
}): boolean {
  if (!opts.showSectionHeaders) return true

  const { section, variant, creatorName, creatorDeleted } = opts
  const hasCreator = Boolean(creatorName?.trim())

  if (section === 'mine' && variant === 'mine') return false
  if (section === 'builtin') return false
  if (section === 'tenantOthers' && variant === 'creator' && !hasCreator && !creatorDeleted) {
    return false
  }

  return true
}

/** Owner / role tag on organization cards. */
export function shouldShowOrgRelationTag(opts: {
  spaceSelection: 'all' | 'created' | 'joined'
  isOwner: boolean
  myRole?: string
}): boolean {
  if (opts.spaceSelection === 'created') return false
  if (opts.spaceSelection === 'joined' && !opts.myRole) return false
  if (opts.spaceSelection === 'all' && opts.isOwner) return false
  if (opts.spaceSelection === 'all' && !opts.isOwner && !opts.myRole) return false
  return true
}
