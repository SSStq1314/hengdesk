import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { modelApi, type UsageGroup, type UsageTotals } from '@/lib/api'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Field, NativeSelect } from './fields'
import { num, yuan } from './format'

function TotalsCells({ t }: { t: UsageTotals }) {
  return (
    <>
      <TableCell className='tabular-nums'>
        {num(t.calls)}
        {t.error_calls > 0 && (
          <span className='text-destructive'>
            {' '}
            ({zh.models.errors} {num(t.error_calls)})
          </span>
        )}
      </TableCell>
      <TableCell className='tabular-nums'>{num(t.prompt_tokens)}</TableCell>
      <TableCell className='tabular-nums'>{num(t.completion_tokens)}</TableCell>
      <TableCell className='tabular-nums'>
        {num(t.total_tokens)}{' '}
        {t.estimated_calls > 0 && (
          <Badge variant='outline'>{zh.models.estimated}</Badge>
        )}
        {(t.unknown_calls ?? 0) > 0 && (
          <Badge variant='outline' title={zh.models.usageUnknownHint}>
            {zh.models.usageUnknown} {num(t.unknown_calls ?? 0)}
          </Badge>
        )}
      </TableCell>
      <TableCell className='tabular-nums'>{yuan(t.cost_micros)}</TableCell>
    </>
  )
}

// 用量统计：按模型 / 人 / 部门汇总 token 与金额；估算用量单独标注（PR3 预算与配额基于同一数据）。
export function UsageTab() {
  const [group, setGroup] = useState<UsageGroup>('model')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const f = { from: from || undefined, to: to || undefined }
  const sum = useQuery({
    queryKey: ['usage-summary', group, f],
    queryFn: () => modelApi.usageSummary(group, f),
  })
  const recs = useQuery({
    queryKey: ['usage-records', f],
    queryFn: () => modelApi.usageRecords(f),
  })
  const tot = sum.data?.totals

  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-end gap-3'>
        <Field label={zh.models.groupBy}>
          <NativeSelect
            value={group}
            onChange={(v) => setGroup(v as UsageGroup)}
            options={[
              { value: 'model', label: zh.models.byModel },
              { value: 'user', label: zh.models.byUser },
              { value: 'department', label: zh.models.byDept },
            ]}
          />
        </Field>
        <Field label={zh.models.from}>
          <Input
            type='date'
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        </Field>
        <Field label={zh.models.to}>
          <Input
            type='date'
            value={to}
            onChange={(e) => setTo(e.target.value)}
          />
        </Field>
      </div>
      {tot && tot.estimated_calls > 0 && (
        <Alert>
          <AlertDescription>
            {zh.models.estimatedHint(tot.estimated_calls, tot.estimated_tokens)}
          </AlertDescription>
        </Alert>
      )}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.models.name}</TableHead>
            <TableHead>{zh.models.calls}</TableHead>
            <TableHead>{zh.models.promptTokens}</TableHead>
            <TableHead>{zh.models.completionTokens}</TableHead>
            <TableHead>{zh.models.totalTokens}</TableHead>
            <TableHead>{zh.models.cost}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(sum.data?.items ?? []).length === 0 && (
            <TableRow>
              <TableCell colSpan={6} className='text-muted-foreground'>
                {zh.models.empty}
              </TableCell>
            </TableRow>
          )}
          {(sum.data?.items ?? []).map((it) => (
            <TableRow key={it.key}>
              <TableCell>
                {it.name ||
                  (group === 'department' ? zh.models.noDept : it.key)}
              </TableCell>
              <TotalsCells t={it} />
            </TableRow>
          ))}
          {tot && (
            <TableRow className='font-medium'>
              <TableCell>{zh.models.total}</TableCell>
              <TotalsCells t={tot} />
            </TableRow>
          )}
        </TableBody>
      </Table>

      <h2 className='text-lg font-semibold'>{zh.models.recent}</h2>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.models.time}</TableHead>
            <TableHead>{zh.models.model}</TableHead>
            <TableHead>{zh.models.purpose}</TableHead>
            <TableHead>{zh.models.status}</TableHead>
            <TableHead>{zh.models.totalTokens}</TableHead>
            <TableHead>{zh.models.cost}</TableHead>
            <TableHead>{zh.models.latency}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(recs.data?.items ?? []).map((r) => (
            <TableRow key={r.id}>
              <TableCell>
                {new Date(r.created_at).toLocaleString('zh-CN')}
              </TableCell>
              <TableCell>{r.model_name}</TableCell>
              <TableCell>{r.purpose}</TableCell>
              <TableCell>
                {r.status}
                {r.error_code ? ` (${r.error_code})` : ''}
              </TableCell>
              <TableCell className='tabular-nums'>
                {num(r.total_tokens)}{' '}
                {r.estimated && (
                  <Badge variant='outline'>{zh.models.estimated}</Badge>
                )}
                {r.usage_unknown && (
                  <Badge variant='outline' title={zh.models.usageUnknownHint}>
                    {zh.models.usageUnknown}
                  </Badge>
                )}
              </TableCell>
              <TableCell className='tabular-nums'>
                {yuan(r.cost_micros ?? 0)}
              </TableCell>
              <TableCell className='tabular-nums'>
                {num(r.latency_ms)} ms
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
