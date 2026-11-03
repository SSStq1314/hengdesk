import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { AlertTriangle } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { systemApi } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

// 管理后台常驻提示（D-33）：未指派安全审计员、未启用 HTTPS（D-30）、备份盘空间不足。只对有系统设置权限的管理员显示。
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
      title: zh.banners.noAuditorTitle,
      desc: zh.banners.noAuditorDesc,
    })
  }
  if (!data.https) {
    items.push({ title: zh.banners.noHttpsTitle, desc: zh.banners.noHttpsDesc })
  }
  if (data.disk_low && data.disk_free_percent != null) {
    items.push({
      title: zh.banners.backupLowTitle,
      desc: zh.banners.backupLowDesc(
        data.disk_free_percent.toFixed(1),
        data.disk_low_threshold,
        data.backup_keep
      ),
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
