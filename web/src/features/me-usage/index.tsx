import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { modelApi } from '@/lib/api'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Input } from '@/components/ui/input'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { yuan } from '@/features/models/format'

const t = zh.meUsage

export function MeUsagePage() {
  const [days, setDays] = useState(30)
  const filter = useMemo(() => {
    const to = new Date()
    const from = new Date()
    from.setDate(to.getDate() - days)
    const fmt = (d: Date) => d.toISOString().slice(0, 10)
    return { from: fmt(from), to: fmt(to) }
  }, [days])
  const q = useQuery({
    queryKey: ['me-usage', filter],
    queryFn: () => modelApi.myUsage(filter),
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
        <div className='mb-4 flex items-end justify-between gap-3'>
          <div>
            <h1 className='text-2xl font-bold'>{t.title}</h1>
            <p className='text-sm text-muted-foreground'>{t.desc}</p>
          </div>
          <Input
            type='number'
            className='w-20'
            value={days}
            onChange={(e) => setDays(Math.max(1, Number(e.target.value) || 30))}
          />
        </div>
        <p className='mb-2 text-sm'>
          {t.tokens} {q.data?.totals.total_tokens ?? 0} · {t.cost}{' '}
          {t.yuan(yuan(q.data?.totals.cost_micros ?? 0))}
        </p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t.colModel}</TableHead>
              <TableHead>{t.colCalls}</TableHead>
              <TableHead>{t.colTokens}</TableHead>
              <TableHead>{t.colCost}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(q.data?.items ?? []).map((r) => (
              <TableRow key={r.key}>
                <TableCell>{r.name || r.key}</TableCell>
                <TableCell>{r.calls}</TableCell>
                <TableCell>{r.total_tokens}</TableCell>
                <TableCell>{t.yuan(yuan(r.cost_micros))}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Main>
    </>
  )
}
