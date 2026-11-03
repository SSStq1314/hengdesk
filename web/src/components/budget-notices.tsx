import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { Wallet } from 'lucide-react'
import { notificationApi, type AppNotification } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { amount } from '@/features/budgets/format'

function describe(n: AppNotification) {
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

// 站内提醒（PR3 预算告警）：发给本人的未读提醒常驻顶部，标记已读后消失。
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
  return (
    <div className='px-4 pt-4' role='status'>
      <Alert>
        <Wallet />
        <AlertTitle>{zh.notices.title(data.unread)}</AlertTitle>
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
