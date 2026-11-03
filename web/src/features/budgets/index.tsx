import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import {
  budgetApi,
  deptApi,
  memberApi,
  modelApi,
  type Budget,
  type BudgetInput,
} from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import { Field, NativeSelect } from '@/features/models/fields'
import { toMicros, yuan } from '@/features/models/format'
import { amount } from './format'

const scopeLabel: Record<string, string> = {
  org: zh.budgets.scopeOrg,
  dept: zh.budgets.scopeDept,
  user: zh.budgets.scopeUser,
  model: zh.budgets.scopeModel,
}

// 编辑态：金额以「元」字符串输入，保存时换算为微元。
type Form = Omit<BudgetInput, 'limit'> & {
  limitText: string
  targetName: string
}
type Edit = { id: number | null; v: Form }

const blank: Form = {
  scope: 'org',
  scope_id: 0,
  period: 'month',
  metric: 'cost',
  limitText: '',
  threshold_pct: 80,
  action: 'block',
  enabled: true,
  note: '',
  targetName: '',
}

const toForm = (b: Budget): Form => ({
  scope: b.scope,
  scope_id: b.scope_id,
  period: b.period,
  metric: b.metric,
  limitText: b.metric === 'cost' ? yuan(b.limit) : String(b.limit),
  threshold_pct: b.threshold_pct,
  action: b.action,
  enabled: b.enabled,
  note: b.note,
  targetName: b.scope_name,
})

const toInput = (f: Form): BudgetInput => ({
  scope: f.scope,
  scope_id: f.scope === 'org' ? 0 : f.scope_id,
  period: f.period,
  metric: f.metric,
  limit: f.metric === 'cost' ? toMicros(f.limitText) : Number(f.limitText),
  threshold_pct: f.threshold_pct,
  action: f.action,
  enabled: f.enabled,
  note: f.note,
})

// 预算与配额管理（M1 第二块 PR3）。仅超级管理员（budget.manage）可见。
export function BudgetsPage() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['budgets'], queryFn: budgetApi.list })
  const [edit, setEdit] = useState<Edit | null>(null)
  const [del, setDel] = useState<Budget | null>(null)
  const [q, setQ] = useState('')
  const onErr = (e: Error) => toast.error(e.message)
  const refresh = () => qc.invalidateQueries({ queryKey: ['budgets'] })
  const depts = useQuery({
    queryKey: ['departments'],
    queryFn: deptApi.list,
    enabled: edit?.v.scope === 'dept',
  })
  const models = useQuery({
    queryKey: ['models'],
    queryFn: modelApi.models,
    enabled: edit?.v.scope === 'model',
  })
  const members = useQuery({
    queryKey: ['members', 'budget-pick', q],
    queryFn: () => memberApi.list({ q }),
    enabled: edit?.v.scope === 'user' && q.trim().length > 0,
  })
  const save = useMutation({
    mutationFn: (e: Edit) =>
      e.id
        ? budgetApi.update(e.id, toInput(e.v))
        : budgetApi.create(toInput(e.v)),
    onSuccess: () => {
      setEdit(null)
      refresh()
    },
    onError: onErr,
  })
  const remove = useMutation({
    mutationFn: budgetApi.remove,
    onSuccess: () => {
      setDel(null)
      refresh()
    },
    onError: onErr,
  })
  const recon = useMutation({
    mutationFn: budgetApi.reconcile,
    onSuccess: (r) => {
      toast.success(
        zh.budgets.reconciled(r.length, r.filter((x) => x.drift !== 0).length)
      )
      refresh()
    },
    onError: onErr,
  })
  const set = (p: Partial<Form>) =>
    setEdit((e) => (e ? { ...e, v: { ...e.v, ...p } } : e))

  let targets: { value: string; label: string }[] = []
  if (edit?.v.scope === 'dept')
    targets = (depts.data?.items ?? []).map((d) => ({
      value: String(d.id),
      label: d.name,
    }))
  if (edit?.v.scope === 'model')
    targets = (models.data?.items ?? []).map((m) => ({
      value: String(m.id),
      label: m.name,
    }))
  if (edit?.v.scope === 'user')
    targets = (members.data?.items ?? []).map((m) => ({
      value: String(m.id),
      label: `${m.display_name} <${m.email}>`,
    }))
  if (
    edit &&
    edit.v.scope_id > 0 &&
    !targets.some((t) => t.value === String(edit.v.scope_id))
  )
    targets.unshift({
      value: String(edit.v.scope_id),
      label: edit.v.targetName || `#${edit.v.scope_id}`,
    })

  const rows = list.data ?? []
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <h1 className='text-2xl font-bold tracking-tight'>
            {zh.budgets.title}
          </h1>
          <div className='ms-auto flex gap-2'>
            <Button
              variant='outline'
              disabled={recon.isPending}
              onClick={() => recon.mutate()}
            >
              {zh.budgets.reconcile}
            </Button>
            <Button onClick={() => setEdit({ id: null, v: blank })}>
              {zh.budgets.new}
            </Button>
          </div>
        </div>
        <p className='mb-4 text-sm text-muted-foreground'>{zh.budgets.desc}</p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{zh.budgets.scope}</TableHead>
              <TableHead>{zh.budgets.target}</TableHead>
              <TableHead>{zh.budgets.period}</TableHead>
              <TableHead>{zh.budgets.used}</TableHead>
              <TableHead>{zh.budgets.threshold}</TableHead>
              <TableHead>{zh.budgets.action}</TableHead>
              <TableHead>{zh.budgets.resetAt}</TableHead>
              <TableHead className='text-end'>{zh.common.actions}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className='text-muted-foreground'>
                  {zh.budgets.empty}
                </TableCell>
              </TableRow>
            )}
            {rows.map((b) => (
              <TableRow key={b.id} className={b.enabled ? '' : 'opacity-60'}>
                <TableCell>
                  <Badge variant='outline'>{scopeLabel[b.scope]}</Badge>
                </TableCell>
                <TableCell>
                  {b.scope === 'org' ? zh.budgets.wholeOrg : b.scope_name}
                  {b.note && (
                    <div className='text-xs text-muted-foreground'>
                      {b.note}
                    </div>
                  )}
                </TableCell>
                <TableCell>
                  {b.period === 'day' ? zh.budgets.day : zh.budgets.month}
                </TableCell>
                <TableCell>
                  <div>
                    {amount(b.metric, b.used)} / {amount(b.metric, b.limit)}
                  </div>
                  <div className='mt-1 h-1.5 w-32 rounded bg-muted'>
                    <div
                      className={
                        'h-1.5 rounded ' +
                        (b.pct >= 100
                          ? 'bg-destructive'
                          : b.pct >= b.threshold_pct
                            ? 'bg-amber-500'
                            : 'bg-primary')
                      }
                      style={{ width: `${Math.min(100, b.pct)}%` }}
                    />
                  </div>
                  <div className='text-xs text-muted-foreground'>
                    {b.pct.toFixed(1)}%{b.pct > 100 && ` · ${zh.budgets.over}`}
                    {b.counter_unavailable && ` · ${zh.budgets.counterDown}`}
                  </div>
                </TableCell>
                <TableCell>{b.threshold_pct}%</TableCell>
                <TableCell>
                  {b.action === 'block'
                    ? zh.budgets.block
                    : zh.budgets.alertOnly}
                </TableCell>
                <TableCell className='text-xs'>
                  {new Date(b.reset_at).toLocaleString('zh-CN')}
                </TableCell>
                <TableCell className='text-end'>
                  <Button
                    size='sm'
                    variant='ghost'
                    onClick={() => setEdit({ id: b.id, v: toForm(b) })}
                  >
                    {zh.common.edit}
                  </Button>
                  <Button size='sm' variant='ghost' onClick={() => setDel(b)}>
                    {zh.common.delete}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Main>

      <Dialog open={!!edit} onOpenChange={(o) => !o && setEdit(null)}>
        <DialogContent className='max-h-[90vh] overflow-y-auto'>
          <DialogHeader>
            <DialogTitle>
              {edit?.id ? zh.budgets.edit : zh.budgets.new}
            </DialogTitle>
          </DialogHeader>
          {edit && (
            <div className='grid gap-3'>
              <Field label={zh.budgets.scope}>
                <NativeSelect
                  value={edit.v.scope}
                  onChange={(v) =>
                    set({
                      scope: v as Form['scope'],
                      scope_id: 0,
                      targetName: '',
                    })
                  }
                  options={Object.entries(scopeLabel).map(([value, label]) => ({
                    value,
                    label,
                  }))}
                />
              </Field>
              {edit.v.scope !== 'org' && (
                <Field label={zh.budgets.target}>
                  {edit.v.scope === 'user' && (
                    <Input
                      className='mb-2'
                      placeholder={zh.models.pickMember}
                      value={q}
                      onChange={(e) => setQ(e.target.value)}
                    />
                  )}
                  <NativeSelect
                    value={edit.v.scope_id ? String(edit.v.scope_id) : ''}
                    onChange={(v) => set({ scope_id: Number(v) })}
                    options={[
                      { value: '', label: zh.budgets.pickTarget },
                      ...targets,
                    ]}
                  />
                </Field>
              )}
              <div className='grid grid-cols-2 gap-3'>
                <Field label={zh.budgets.period}>
                  <NativeSelect
                    value={edit.v.period}
                    onChange={(v) => set({ period: v as Form['period'] })}
                    options={[
                      { value: 'day', label: zh.budgets.day },
                      { value: 'month', label: zh.budgets.month },
                    ]}
                  />
                </Field>
                <Field label={zh.budgets.metric}>
                  <NativeSelect
                    value={edit.v.metric}
                    onChange={(v) => set({ metric: v as Form['metric'] })}
                    options={[
                      { value: 'cost', label: zh.budgets.cost },
                      { value: 'tokens', label: zh.budgets.tokens },
                    ]}
                  />
                </Field>
                <Field
                  label={`${zh.budgets.limit}（${edit.v.metric === 'cost' ? zh.budgets.cost : zh.budgets.tokens}）`}
                >
                  <Input
                    inputMode='decimal'
                    value={edit.v.limitText}
                    onChange={(e) => set({ limitText: e.target.value })}
                  />
                </Field>
                <Field label={zh.budgets.threshold}>
                  <Input
                    type='number'
                    min={1}
                    max={100}
                    value={edit.v.threshold_pct}
                    onChange={(e) =>
                      set({ threshold_pct: Number(e.target.value) })
                    }
                  />
                </Field>
                <Field label={zh.budgets.action}>
                  <NativeSelect
                    value={edit.v.action}
                    onChange={(v) => set({ action: v as Form['action'] })}
                    options={[
                      { value: 'block', label: zh.budgets.block },
                      { value: 'alert_only', label: zh.budgets.alertOnly },
                    ]}
                  />
                </Field>
              </div>
              <Field label={zh.budgets.note}>
                <Input
                  maxLength={200}
                  value={edit.v.note}
                  onChange={(e) => set({ note: e.target.value })}
                />
              </Field>
              <label className='flex items-center gap-2 text-sm'>
                <Switch
                  checked={edit.v.enabled}
                  onCheckedChange={(c) => set({ enabled: c })}
                />
                {zh.common.enabled}
              </label>
            </div>
          )}
          <DialogFooter>
            <Button variant='outline' onClick={() => setEdit(null)}>
              {zh.common.cancel}
            </Button>
            <Button
              disabled={save.isPending}
              onClick={() => edit && save.mutate(edit)}
            >
              {zh.common.save}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={!!del}
        onOpenChange={(o) => !o && setDel(null)}
        title={zh.common.delete}
        desc={zh.budgets.deleteConfirm}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => del && remove.mutate(del.id)}
      />
    </>
  )
}
