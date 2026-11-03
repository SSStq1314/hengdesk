import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
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
        <h1 className='mb-4 text-2xl font-bold tracking-tight'>
          {zh.audit.title}
        </h1>
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <Input
            className='w-48'
            placeholder={zh.audit.actorPlaceholder}
            value={draft.actor ?? ''}
            onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
          />
          <Input
            className='w-48'
            placeholder={zh.audit.actionPlaceholder}
            value={draft.action ?? ''}
            onChange={(e) => setDraft({ ...draft, action: e.target.value })}
          />
          <Button onClick={() => setFilter({ ...draft })}>
            {zh.common.query}
          </Button>
          <Button variant='outline' onClick={() => verify.refetch()}>
            {zh.audit.verify}
          </Button>
          {perms.includes('audit.export') && (
            <Button variant='outline' asChild>
              <a href={auditApi.exportUrl(filter)}>{zh.audit.exportCsv}</a>
            </Button>
          )}
          {verify.data &&
            (verify.data.ok ? (
              <Badge>{zh.audit.chainOk(verify.data.checked)}</Badge>
            ) : (
              <Badge variant='destructive'>
                {zh.audit.chainBroken(verify.data.broken_at)}
              </Badge>
            ))}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{zh.audit.colTime}</TableHead>
              <TableHead>{zh.audit.colActor}</TableHead>
              <TableHead>{zh.audit.colAction}</TableHead>
              <TableHead>{zh.audit.colOutcome}</TableHead>
              <TableHead>{zh.audit.colResource}</TableHead>
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
            {zh.common.earlier}
          </Button>
        )}
      </Main>
    </>
  )
}
