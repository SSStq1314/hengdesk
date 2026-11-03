import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { ShieldAlert } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { alertApi, type HighRiskAlert } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

function describe(a: HighRiskAlert) {
  const ts = new Date(a.ts).toLocaleString('zh-CN')
  if (a.detail?.kind === 'break_glass_login') {
    return zh.alerts.breakGlassLogin(a.actor_email, ts, a.ip)
  }
  return zh.alerts.other(a.detail?.kind ?? '', a.actor_email, ts)
}

// 审计员站内通知（D-34）：未读的高风险事件常驻提醒，标记已读后消失。只对有审计查看权限的用户显示。
export function HighRiskAlerts() {
  const user = useAuthStore((s) => s.auth.user)
  const allowed = !!user?.permissions?.includes('audit.read')
  const qc = useQueryClient()
  const { data } = useQuery({
    queryKey: ['high-risk-alerts'],
    queryFn: alertApi.list,
    enabled: allowed,
    refetchInterval: 60 * 1000,
  })
  const read = useMutation({
    mutationFn: alertApi.markRead,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['high-risk-alerts'] }),
  })
  if (!allowed || !data || data.unread === 0) return null
  const shown = data.items.slice(0, 5)
  return (
    <div className='px-4 pt-4' role='alert'>
      <Alert variant='destructive'>
        <ShieldAlert />
        <AlertTitle>{zh.alerts.title(data.unread)}</AlertTitle>
        <AlertDescription>
          <ul className='list-disc ps-4'>
            {shown.map((a) => (
              <li key={a.id}>{describe(a)}</li>
            ))}
          </ul>
          {data.unread > shown.length && (
            <p>{zh.alerts.more(data.unread - shown.length)}</p>
          )}
          <Button
            size='sm'
            variant='outline'
            className='mt-2'
            disabled={read.isPending}
            onClick={() => read.mutate(data.items[0].id)}
          >
            {zh.alerts.markRead}
          </Button>
        </AlertDescription>
      </Alert>
    </div>
  )
}
