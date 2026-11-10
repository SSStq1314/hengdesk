import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { zh } from '@/locales/zh-CN'
import { useAuthStore } from '@/stores/auth-store'
import { modelApi, type UsageFilter } from '@/lib/api'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
import { ConfigDrawer } from '@/components/config-drawer'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { yuan } from '@/features/models/format'

function dayRange(days: number): UsageFilter {
  const to = new Date()
  const from = new Date()
  from.setDate(to.getDate() - days)
  const fmt = (d: Date) => d.toISOString().slice(0, 10)
  return { from: fmt(from), to: fmt(to) }
}

export function Dashboard() {
  const user = useAuthStore((s) => s.auth.user)
  const perms = user?.permissions ?? []
  const canStats = perms.includes('budget.read')
  const [days, setDays] = useState(7)
  const filter = useMemo(() => dayRange(days), [days])

  const overview = useQuery({
    queryKey: ['stats-overview', filter],
    queryFn: () => modelApi.statsOverview(filter),
    enabled: canStats,
  })
  const series = useQuery({
    queryKey: ['stats-ts', filter],
    queryFn: () =>
      modelApi.statsTimeseries({ ...filter, grain: 'day', group_by: 'total' }),
    enabled: canStats,
  })
  const topUsers = useQuery({
    queryKey: ['stats-users', filter],
    queryFn: () => modelApi.statsSummary('user', filter),
    enabled: canStats,
  })

  const chart = (series.data?.points ?? []).map((p) => ({
    bucket: p.bucket.slice(5),
    tokens: p.totals.total_tokens,
    cost: Number(yuan(p.totals.cost_micros)),
  }))

  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ConfigDrawer />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <div className='mb-4 flex flex-wrap items-end justify-between gap-3'>
          <div>
            <h1 className='text-2xl font-bold tracking-tight'>
              {zh.dashboard.title}
            </h1>
            <p className='text-sm text-muted-foreground'>
              {zh.dashboard.welcome(user?.display_name || user?.email || '')}
            </p>
          </div>
          {canStats && (
            <div className='flex items-center gap-2'>
              <label className='text-sm text-muted-foreground'>
                {zh.dashboard.rangeDays}
              </label>
              <Input
                type='number'
                min={1}
                max={90}
                className='w-20'
                value={days}
                onChange={(e) => setDays(Math.max(1, Number(e.target.value) || 7))}
              />
              <Button
                variant='outline'
                size='sm'
                onClick={() => {
                  window.location.href = modelApi.statsExportUrl(filter)
                }}
              >
                {zh.dashboard.export}
              </Button>
            </div>
          )}
        </div>

        {!canStats ? (
          <Card>
            <CardHeader>
              <CardTitle>{zh.dashboard.noPermTitle}</CardTitle>
              <CardDescription>{zh.dashboard.noPermDesc}</CardDescription>
            </CardHeader>
          </Card>
        ) : (
          <>
            <div className='mb-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4'>
              {[
                {
                  k: zh.dashboard.activeUsers,
                  v: overview.data?.active_users ?? '—',
                },
                {
                  k: zh.dashboard.conversations,
                  v: overview.data?.conversations ?? '—',
                },
                {
                  k: zh.dashboard.tokens,
                  v: overview.data?.totals.total_tokens ?? '—',
                },
                {
                  k: zh.dashboard.cost,
                  v: overview.data
                    ? zh.dashboard.yuan(yuan(overview.data.totals.cost_micros))
                    : '—',
                },
              ].map((c) => (
                <Card key={c.k}>
                  <CardHeader className='pb-2'>
                    <CardDescription>{c.k}</CardDescription>
                    <CardTitle className='text-2xl'>{c.v}</CardTitle>
                  </CardHeader>
                </Card>
              ))}
            </div>
            {(overview.data?.totals.estimated_calls ?? 0) > 0 && (
              <p className='mb-3 text-xs text-amber-600'>
                {zh.dashboard.estimatedHint(
                  overview.data!.totals.estimated_calls,
                  overview.data!.totals.estimated_tokens
                )}
              </p>
            )}
            <div className='grid gap-4 lg:grid-cols-2'>
              <Card>
                <CardHeader>
                  <CardTitle>{zh.dashboard.trend}</CardTitle>
                </CardHeader>
                <CardContent className='h-64'>
                  <ResponsiveContainer width='100%' height='100%'>
                    <BarChart data={chart}>
                      <CartesianGrid strokeDasharray='3 3' />
                      <XAxis dataKey='bucket' />
                      <YAxis />
                      <Tooltip />
                      <Bar dataKey='tokens' name={zh.dashboard.tokens} fill='var(--chart-1)' />
                    </BarChart>
                  </ResponsiveContainer>
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle>{zh.dashboard.topUsers}</CardTitle>
                </CardHeader>
                <CardContent>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{zh.dashboard.colUser}</TableHead>
                        <TableHead>{zh.dashboard.tokens}</TableHead>
                        <TableHead>{zh.dashboard.cost}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {(topUsers.data?.items ?? []).slice(0, 10).map((r) => (
                        <TableRow key={r.key}>
                          <TableCell>{r.name || r.key}</TableCell>
                          <TableCell>{r.total_tokens}</TableCell>
                          <TableCell>{zh.dashboard.yuan(yuan(r.cost_micros))}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            </div>
          </>
        )}
      </Main>
    </>
  )
}
