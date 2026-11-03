import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { useAuthStore } from '@/stores/auth-store'
import { auditApi, type AuditFilter } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
  const [traceId, setTraceId] = useState<string | null>(null)
  const trace = useQuery({
    queryKey: ['audit-trace', traceId],
    queryFn: () => auditApi.trace(traceId!),
    enabled: !!traceId,
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
          <Input
            className='w-72 font-mono'
            placeholder={zh.audit.tracePlaceholder}
            value={draft.trace_id ?? ''}
            onChange={(e) =>
              setDraft({ ...draft, trace_id: e.target.value.trim() })
            }
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
              <TableHead>{zh.audit.colTrace}</TableHead>
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
                <TableCell>
                  {r.trace_id && (
                    <Button
                      size='sm'
                      variant='link'
                      className='h-auto p-0'
                      onClick={() => setTraceId(r.trace_id!)}
                    >
                      {zh.audit.viewTrace}
                    </Button>
                  )}
                </TableCell>
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
        <Dialog open={!!traceId} onOpenChange={(o) => !o && setTraceId(null)}>
          <DialogContent className='max-h-[80vh] overflow-y-auto sm:max-w-2xl'>
            <DialogHeader>
              <DialogTitle>{zh.audit.traceTitle}</DialogTitle>
            </DialogHeader>
            <p className='font-mono text-xs break-all text-muted-foreground'>
              {traceId}
            </p>
            {trace.error && (
              <p className='text-sm text-destructive'>{trace.error.message}</p>
            )}
            {trace.data && (
              <>
                <p className='text-sm'>
                  {zh.audit.traceSummary(
                    trace.data.count,
                    trace.data.duration_ms
                  )}
                </p>
                {trace.data.truncated && (
                  <p className='text-xs text-amber-600'>
                    {zh.audit.traceTruncated}
                  </p>
                )}
                <ol className='border-s ps-4'>
                  {trace.data.items.map((s) => (
                    <li key={s.id} className='mb-3'>
                      <div className='text-xs text-muted-foreground'>
                        +{s.offset_ms} ms ·{' '}
                        {new Date(s.ts).toLocaleTimeString('zh-CN')}
                      </div>
                      <div className='text-sm'>
                        <span className='font-medium'>{s.action}</span>{' '}
                        <Badge variant='outline'>{s.outcome || s.phase}</Badge>
                      </div>
                      <div className='text-xs text-muted-foreground'>
                        {s.actor_email || '—'}
                        {s.resource_id ? ` · ${s.resource_id}` : ''}
                      </div>
                    </li>
                  ))}
                </ol>
              </>
            )}
          </DialogContent>
        </Dialog>
      </Main>
    </>
  )
}
