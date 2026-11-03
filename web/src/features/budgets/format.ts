import { zh } from '@/locales/zh-CN'
import { num, yuan } from '@/features/models/format'

// 预算数值：cost 为微元，tokens 为个数。
export const amount = (metric: string | undefined, v: number | undefined) =>
  metric === 'cost'
    ? `${yuan(v ?? 0)} ${zh.budgets.cost}`
    : `${num(v)} ${zh.budgets.tokens}`
