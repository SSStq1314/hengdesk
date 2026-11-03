import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { egressApi, type EgressLogFilter } from '@/lib/api'
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
import { NativeSelect } from '@/features/models/fields'

export function LogsTab(props: { readOnly: boolean }) {
  const [draft, setDraft] = useState<EgressLogFilter>({})
  const [filter, setFilter] = useState<EgressLogFilter>({})
  const list = useQuery({
    queryKey: ['egress-logs', filter],
    queryFn: () => egressApi.logs(filter),
  })
  return (
    <>
      {props.readOnly && (
        <p className='mb-3 text-sm text-muted-foreground'>
          {zh.egress.readOnly}
        </p>
      )}
      <div className='mb-4 flex flex-wrap items-center gap-2'>
        <NativeSelect
          ariaLabel={zh.egress.colDecision}
          value={draft.decision ?? ''}
          onChange={(v) => setDraft({ ...draft, decision: v })}
          options={[
            { value: '', label: zh.egress.allDecisions },
            { value: 'allow', label: zh.egress.allow },
            { value: 'deny', label: zh.egress.deny },
          ]}
        />
        <Input
          className='w-48'
          placeholder={zh.egress.hostPlaceholder}
          value={draft.host ?? ''}
          onChange={(e) => setDraft({ ...draft, host: e.target.value })}
        />
        <Input
          className='w-40'
          placeholder={zh.egress.modulePlaceholder}
          value={draft.module ?? ''}
          onChange={(e) => setDraft({ ...draft, module: e.target.value })}
        />
        <Button onClick={() => setFilter({ ...draft })}>
          {zh.common.query}
        </Button>
        <Button variant='outline' asChild title={zh.egress.exportHint}>
          <a href={egressApi.exportUrl(filter)}>{zh.egress.export}</a>
        </Button>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.egress.colTime}</TableHead>
            <TableHead>{zh.egress.colUser}</TableHead>
            <TableHead>{zh.egress.colModule}</TableHead>
            <TableHead>{zh.egress.colTarget}</TableHead>
            <TableHead>{zh.egress.colDecision}</TableHead>
            <TableHead>{zh.egress.colReason}</TableHead>
            <TableHead>{zh.egress.colStatus}</TableHead>
            <TableHead>{zh.egress.colDuration}</TableHead>
            <TableHead>{zh.egress.colBytes}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.data?.items.map((r) => (
            <TableRow key={r.id}>
              <TableCell>{new Date(r.ts).toLocaleString('zh-CN')}</TableCell>
              <TableCell>{r.actor_email || '—'}</TableCell>
              <TableCell>{r.module || '—'}</TableCell>
              <TableCell className='font-mono text-xs'>
                {r.method} {r.scheme}://{r.host}
                {r.port ? `:${r.port}` : ''}
                {r.path}
                {r.ip && ` (${r.ip})`}
              </TableCell>
              <TableCell>
                {r.decision === 'allow' ? (
                  <Badge>{zh.egress.allow}</Badge>
                ) : (
                  <Badge variant='destructive'>{zh.egress.deny}</Badge>
                )}
              </TableCell>
              <TableCell>{r.reason_text || r.error || '—'}</TableCell>
              <TableCell>{r.status || '—'}</TableCell>
              <TableCell>{r.duration_ms} ms</TableCell>
              <TableCell>
                {r.bytes_out} / {r.bytes_in}
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
    </>
  )
}
