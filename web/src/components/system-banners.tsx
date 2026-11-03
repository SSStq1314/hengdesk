import { useQuery } from '@tanstack/react-query'
import { AlertTriangle } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { systemApi } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

// 管理后台常驻提示（D-33）：未指派安全审计员、备份盘空间不足。只对有系统设置权限的管理员显示。
export function SystemBanners() {
  const user = useAuthStore((s) => s.auth.user)
  const allowed = !!user?.permissions?.includes('system.settings')
  const { data } = useQuery({
    queryKey: ['system-status'],
    queryFn: systemApi.status,
    enabled: allowed,
    refetchInterval: 5 * 60 * 1000,
  })
  if (!allowed || !data) return null
  const items: { title: string; desc: string }[] = []
  if (!data.auditor_assigned) {
    items.push({
      title: '尚未指派安全审计员',
      desc: '三员分立要求由独立的安全审计员查看审计日志。请在「角色」中为一名非超级管理员的成员授予安全审计员角色。',
    })
  }
  if (data.disk_low && data.disk_free_percent != null) {
    items.push({
      title: '备份盘空间不足',
      desc: `备份目录剩余空间 ${data.disk_free_percent.toFixed(1)}%，低于 ${data.disk_low_threshold}% 阈值。升级前备份失败会中止升级，请尽快清理或扩容（系统自动保留最近 ${data.backup_keep} 份备份）。`,
    })
  }
  if (items.length === 0) return null
  return (
    <div className='space-y-2 px-4 pt-4' role='status'>
      {items.map((i) => (
        <Alert key={i.title} variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{i.title}</AlertTitle>
          <AlertDescription>{i.desc}</AlertDescription>
        </Alert>
      ))}
    </div>
  )
}
