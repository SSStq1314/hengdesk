import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { egressApi, type EgressBlocked } from '@/lib/api'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ConfirmDialog } from '@/components/confirm-dialog'

const isIP = (h: string) => /^[\d.]+$/.test(h) || h.includes(':')

// 被拦截的出站请求（D-39 第 1 条）：按主机 + 端口 + 原因聚合；超级管理员可一键把主机:端口加入白名单（二次确认）。
export function BlockedTab({ canManage }: { canManage: boolean }) {
  const qc = useQueryClient()
  const days = 7
  const list = useQuery({
    queryKey: ['egress-blocked', days],
    queryFn: () => egressApi.blocked(days),
  })
  const [add, setAdd] = useState<EgressBlocked | null>(null)
  const create = useMutation({
    mutationFn: (b: EgressBlocked) =>
      egressApi.create({
        kind: isIP(b.host) ? 'ip' : 'host',
        value: b.host,
        ports: String(b.port),
        note: zh.egress.blockedAddNote(b.count),
      }),
    onSuccess: () => {
      setAdd(null)
      toast.success(zh.egress.blockedAdded)
      qc.invalidateQueries({ queryKey: ['egress-rules'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const items = list.data?.items ?? []
  return (
    <>
      <p className='mb-3 text-sm text-muted-foreground'>
        {zh.egress.blockedIntro(days)}
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.egress.value}</TableHead>
            <TableHead>{zh.egress.ports}</TableHead>
            <TableHead>{zh.egress.blockedReason}</TableHead>
            <TableHead>{zh.egress.blockedCount}</TableHead>
            <TableHead>{zh.egress.blockedLast}</TableHead>
            <TableHead>{zh.egress.blockedModule}</TableHead>
            {canManage && <TableHead>{zh.common.actions}</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.isSuccess && items.length === 0 && (
            <TableRow>
              <TableCell colSpan={7} className='text-muted-foreground'>
                {zh.egress.blockedEmpty}
              </TableCell>
            </TableRow>
          )}
          {items.map((b) => (
            <TableRow key={`${b.host}:${b.port}:${b.reason}`}>
              <TableCell className='font-mono'>{b.host}</TableCell>
              <TableCell>{b.port}</TableCell>
              <TableCell>{b.reason_text || b.reason}</TableCell>
              <TableCell>{b.count}</TableCell>
              <TableCell>
                {new Date(b.last_ts).toLocaleString('zh-CN')}
              </TableCell>
              <TableCell>{b.last_module}</TableCell>
              {canManage && (
                <TableCell>
                  <Button size='sm' variant='outline' onClick={() => setAdd(b)}>
                    {zh.egress.blockedAdd}
                  </Button>
                </TableCell>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <ConfirmDialog
        open={!!add}
        onOpenChange={(o) => !o && setAdd(null)}
        title={zh.egress.blockedAdd}
        desc={add ? `${add.host}:${add.port}` : ''}
        confirmText={zh.egress.blockedAdd}
        isLoading={create.isPending}
        handleConfirm={() => add && create.mutate(add)}
      />
    </>
  )
}
