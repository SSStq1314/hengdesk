import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { Bell, ClipboardCheck, Wallet } from 'lucide-react'
import { notificationApi, type AppNotification } from '@/lib/api'
import {
  describeNotification,
  NOTICE_KIND_APPROVAL,
  NOTICE_KIND_BUDGET,
  unreadNoticesTitle,
} from '@/lib/notice-text'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { amount } from '@/features/budgets/format'

function budgetText(n: AppNotification) {
  const d = n.detail ?? {}
  const period = d.period === 'month' ? zh.notices.month : zh.notices.day
  return zh.notices.budget(
    d.scope_name ?? '',
    period,
    amount(d.metric, d.used),
    amount(d.metric, d.limit),
    d.level ?? 0
  )
}

function describe(n: AppNotification) {
  return describeNotification(n, () => budgetText(n))
}

function NoticeIcon({ kinds }: { kinds: string[] }) {
  const unique = new Set(kinds)
  if (unique.size === 1 && unique.has(NOTICE_KIND_BUDGET)) return <Wallet />
  if (unique.size === 1 && unique.has(NOTICE_KIND_APPROVAL))
    return <ClipboardCheck />
  return <Bell />
}

// 站内提醒：发给本人的未读提醒常驻顶部，按类型分别渲染，标记已读后消失。
export function BudgetNotices() {
  const qc = useQueryClient()
  const { data } = useQuery({
    queryKey: ['notifications'],
    queryFn: notificationApi.list,
    refetchInterval: 60 * 1000,
  })
  const read = useMutation({
    mutationFn: notificationApi.markRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  })
  if (!data || data.unread === 0) return null
  const shown = data.items.filter((n) => !n.read).slice(0, 5)
  const kinds = shown.map((n) => n.kind)
  return (
    <div className='px-4 pt-4' role='status'>
      <Alert>
        <NoticeIcon kinds={kinds} />
        <AlertTitle>{unreadNoticesTitle(data.unread, kinds)}</AlertTitle>
        <AlertDescription>
          <ul className='list-disc ps-4'>
            {shown.map((n) => (
              <li key={n.id}>
                {new Date(n.created_at).toLocaleString('zh-CN')} {describe(n)}
              </li>
            ))}
          </ul>
          <Button
            size='sm'
            variant='outline'
            className='mt-2'
            disabled={read.isPending}
            onClick={() => read.mutate()}
          >
            {zh.notices.markRead}
          </Button>
        </AlertDescription>
      </Alert>
    </div>
  )
}
