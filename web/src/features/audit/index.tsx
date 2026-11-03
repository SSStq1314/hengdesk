import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { auditApi, type AuditFilter } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

export function AuditPage() {
  const perms = useAuthStore((s) => s.auth.user?.permissions ?? [])
  const [draft, setDraft] = useState<AuditFilter>({})
  const [filter, setFilter] = useState<AuditFilter>({})
  const list = useQuery({
    queryKey: ['audit', filter],
    queryFn: () => auditApi.list(filter),
  })
  const verify = useQuery({
    queryKey: ['audit-verify'],
    queryFn: auditApi.verify,
    enabled: false,
  })
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <h1 className='mb-4 text-2xl font-bold tracking-tight'>审计日志</h1>
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <Input
            className='w-48'
            placeholder='操作者邮箱'
            value={draft.actor ?? ''}
            onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
          />
          <Input
            className='w-48'
            placeholder='动作前缀，如 auth.'
            value={draft.action ?? ''}
            onChange={(e) => setDraft({ ...draft, action: e.target.value })}
          />
          <Button onClick={() => setFilter({ ...draft })}>查询</Button>
          <Button variant='outline' onClick={() => verify.refetch()}>
            校验哈希链
          </Button>
          {perms.includes('audit.export') && (
            <Button variant='outline' asChild>
              <a href={auditApi.exportUrl(filter)}>导出 CSV</a>
            </Button>
          )}
          {verify.data &&
            (verify.data.ok ? (
              <Badge>链完整，已校验 {verify.data.checked} 条</Badge>
            ) : (
              <Badge variant='destructive'>
                链断裂于 #{verify.data.broken_at}
              </Badge>
            ))}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>时间</TableHead>
              <TableHead>操作者</TableHead>
              <TableHead>动作</TableHead>
              <TableHead>结果</TableHead>
              <TableHead>对象</TableHead>
              <TableHead>IP</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.data?.items.map((r) => (
              <TableRow key={r.id}>
                <TableCell>{new Date(r.ts).toLocaleString('zh-CN')}</TableCell>
                <TableCell>{r.actor_email || '—'}</TableCell>
                <TableCell>{r.action}</TableCell>
                <TableCell>{r.outcome || r.phase}</TableCell>
                <TableCell>{r.resource_id}</TableCell>
                <TableCell>{r.ip}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {!!list.data?.next_before_id && (
          <Button
            variant='outline'
            className='mt-4'
            onClick={() =>
              setFilter({ ...filter, before_id: list.data.next_before_id })
            }
          >
            更早
          </Button>
        )}
      </Main>
    </>
  )
}
