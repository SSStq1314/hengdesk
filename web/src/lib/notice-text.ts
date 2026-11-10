// 站内提醒文案：按通知类型分别渲染，不再一律套用「预算提醒」模板。

export const NOTICE_KIND_BUDGET = 'budget_alert'
export const NOTICE_KIND_APPROVAL = 'approval'

export type NoticeLike = {
  kind: string
  title: string
}

export type NoticeTitles = {
  budget: (n: number) => string
  approval: (n: number) => string
  mixed: (n: number) => string
}

// describeNotification 预算告警用 formatBudget 生成用量说明；其它类型（审批等）直接用服务端标题。
export function describeNotification(
  n: NoticeLike,
  formatBudget: () => string
): string {
  if (n.kind === NOTICE_KIND_BUDGET) return formatBudget()
  return n.title || ''
}

// unreadNoticesTitle 横幅标题随未读类型变化，避免审批通知也写成「预算提醒」。
export function unreadNoticesTitle(
  unread: number,
  kinds: string[],
  titles: NoticeTitles
): string {
  const unique = new Set(kinds)
  if (unique.size === 1 && unique.has(NOTICE_KIND_BUDGET)) {
    return titles.budget(unread)
  }
  if (unique.size === 1 && unique.has(NOTICE_KIND_APPROVAL)) {
    return titles.approval(unread)
  }
  return titles.mixed(unread)
}
