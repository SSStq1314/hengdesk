import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { retentionApi, type LegalHold } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { Field } from '@/features/models/fields'

const t = zh.retention

export function RetentionPage() {
  const qc = useQueryClient()
  const onErr = (e: Error) => toast.error(e.message)
  const cfg = useQuery({ queryKey: ['retention'], queryFn: retentionApi.get })
  const holds = useQuery({
    queryKey: ['legal-holds'],
    queryFn: retentionApi.holds,
  })
  const [draft, setDraft] = useState<{
    enabled: boolean
    days: string
  } | null>(null)
  const enabled = draft?.enabled ?? cfg.data?.settings.enabled ?? false
  const days = draft?.days ?? String(cfg.data?.settings.days ?? 365)
  const setEnabled = (v: boolean) => setDraft({ enabled: v, days })
  const setDays = (v: string) => setDraft({ enabled, days: v })
  const [email, setEmail] = useState('')
  const [reason, setReason] = useState('')
  const [runOpen, setRunOpen] = useState(false)
  const [release, setRelease] = useState<LegalHold | null>(null)
  const save = useMutation({
    mutationFn: () => retentionApi.save({ enabled, days: Number(days) }),
    onSuccess: () => {
      toast.success(t.saved)
      setDraft(null)
      qc.invalidateQueries({ queryKey: ['retention'] })
    },
    onError: onErr,
  })
  const run = useMutation({
    mutationFn: retentionApi.run,
    onSuccess: (r) => {
      setRunOpen(false)
      toast.success(t.runDone(r.conversations, r.messages, r.files))
    },
    onError: (e: Error) => {
      setRunOpen(false)
      onErr(e)
    },
  })
  const refreshHolds = () => qc.invalidateQueries({ queryKey: ['legal-holds'] })
  const add = useMutation({
    mutationFn: () =>
      retentionApi.createHold({
        user_email: email.trim() || undefined,
        reason: reason.trim(),
      }),
    onSuccess: () => {
      setEmail('')
      setReason('')
      refreshHolds()
    },
    onError: onErr,
  })
  const rel = useMutation({
    mutationFn: (id: number) => retentionApi.releaseHold(id),
    onSuccess: () => {
      setRelease(null)
      refreshHolds()
    },
    onError: onErr,
  })
  const minDays = cfg.data?.min_days ?? 180
  const defDays = cfg.data?.default_days ?? 365
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <h1 className='mb-2 text-2xl font-bold tracking-tight'>{t.title}</h1>
        <p className='mb-4 max-w-3xl text-sm text-muted-foreground'>{t.desc}</p>
        <div className='mb-8 grid max-w-md gap-4'>
          <label className='flex items-center gap-2 text-sm'>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
            {t.enabled}
          </label>
          <Field label={t.days(minDays, defDays)}>
            <Input
              type='number'
              min={minDays}
              value={days}
              onChange={(e) => setDays(e.target.value)}
            />
          </Field>
          <div className='flex gap-2'>
            <Button onClick={() => save.mutate()} disabled={save.isPending}>
              {t.save}
            </Button>
            <Button
              variant='destructive'
              disabled={!cfg.data?.settings.enabled}
              onClick={() => setRunOpen(true)}
            >
              {t.run}
            </Button>
          </div>
          {cfg.data && !cfg.data.settings.enabled && (
            <p className='text-xs text-muted-foreground'>{t.disabledHint}</p>
          )}
        </div>

        <h2 className='mb-2 text-lg font-semibold'>{t.holdsTitle}</h2>
        <p className='mb-4 max-w-3xl text-sm text-muted-foreground'>
          {t.holdsDesc}
        </p>
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <Input
            className='w-64'
            placeholder={t.holdEmail}
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <Input
            className='w-72'
            placeholder={t.holdReason}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <Button
            onClick={() => add.mutate()}
            disabled={!reason.trim() || add.isPending}
          >
            {t.addHold}
          </Button>
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t.colScope}</TableHead>
              <TableHead>{t.colReason}</TableHead>
              <TableHead>{t.colCreated}</TableHead>
              <TableHead>{t.colStatus}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {holds.data?.items.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className='text-muted-foreground'>
                  {t.empty}
                </TableCell>
              </TableRow>
            )}
            {holds.data?.items.map((h) => (
              <TableRow key={h.id}>
                <TableCell>
                  {h.scope === 'org' ? t.scopeOrg : h.user_email || h.user_id}
                </TableCell>
                <TableCell>{h.reason}</TableCell>
                <TableCell>
                  {new Date(h.created_at).toLocaleString('zh-CN')}
                </TableCell>
                <TableCell>
                  {h.released_at ? (
                    <Badge variant='outline'>{t.released}</Badge>
                  ) : (
                    <Badge>{t.active}</Badge>
                  )}
                </TableCell>
                <TableCell className='text-right'>
                  {!h.released_at && (
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => setRelease(h)}
                    >
                      {t.release}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <ConfirmDialog
          open={runOpen}
          onOpenChange={setRunOpen}
          title={t.run}
          desc={t.runConfirm}
          destructive
          isLoading={run.isPending}
          handleConfirm={() => run.mutate()}
        />
        <ConfirmDialog
          open={!!release}
          onOpenChange={(o) => !o && setRelease(null)}
          title={t.release}
          desc={t.releaseConfirm}
          isLoading={rel.isPending}
          handleConfirm={() => release && rel.mutate(release.id)}
        />
      </Main>
    </>
  )
}
