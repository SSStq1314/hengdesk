import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { AlertTriangle } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import { systemApi } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'

// 管理后台常驻提示（D-33）：另含主密钥未确认备份（D-36）：未指派安全审计员、未启用 HTTPS（D-30）、自签证书与证书到期（D-34）、备份盘空间不足。只对有系统设置权限的管理员显示。
export function SystemBanners() {
  const user = useAuthStore((s) => s.auth.user)
  const allowed = !!user?.permissions?.includes('system.settings')
  const qc = useQueryClient()
  const [ackOpen, setAckOpen] = useState(false)
  const ack = useMutation({
    mutationFn: systemApi.ackMasterKey,
    onSuccess: (st) => {
      qc.setQueryData(['system-status'], st)
      setAckOpen(false)
      toast.success(zh.banners.masterKeyAcked)
    },
    onError: (e: Error) => toast.error(e.message),
  })
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
  const tls = data.tls
  if (tls?.present && tls.self_signed) {
    items.push({
      title: zh.banners.selfSignedTitle,
      desc: zh.banners.selfSignedDesc,
    })
  }
  if (tls?.present && tls.expiring && tls.not_after) {
    items.push({
      title: tls.expired
        ? zh.banners.certExpiredTitle
        : zh.banners.certExpiringTitle(tls.days_left),
      desc: zh.banners.certExpiryDesc(
        new Date(tls.not_after).toLocaleString('zh-CN')
      ),
    })
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
  const mk = data.master_key
  const mkPending = !!mk?.configured && !mk.acknowledged
  if (items.length === 0 && !mkPending) return null
  return (
    <div className='space-y-2 px-4 pt-4' role='status'>
      {mk && mkPending && (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{zh.banners.masterKeyTitle}</AlertTitle>
          <AlertDescription>
            <p className='break-all'>
              {zh.banners.masterKeyDesc(mk.path, mk.warning)}
            </p>
            <Button
              size='sm'
              variant='outline'
              className='mt-2'
              onClick={() => setAckOpen(true)}
            >
              {zh.banners.masterKeyAck}
            </Button>
          </AlertDescription>
          <ConfirmDialog
            open={ackOpen}
            onOpenChange={setAckOpen}
            title={zh.banners.masterKeyAckTitle}
            desc={zh.banners.masterKeyAckDesc(mk.fingerprint)}
            confirmText={zh.banners.masterKeyAck}
            isLoading={ack.isPending}
            handleConfirm={() => ack.mutate(mk.fingerprint)}
          />
        </Alert>
      )}
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
