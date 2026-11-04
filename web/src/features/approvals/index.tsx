import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { approvalApi, type Approval } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { RiskBadge } from '@/features/mcp/tools-tab'
import { NativeSelect } from '@/features/models/fields'

const t = zh.approvals
const route = getRouteApi('/_authenticated/approvals')
const fmt = (s?: string | null) => (s ? new Date(s).toLocaleString() : '')

export function StatusBadge({ a }: { a: Approval }) {
  const v =
    a.status === 'pending'
      ? 'secondary'
      : a.status === 'approved'
        ? 'default'
        : 'outline'
  return (
    <span className='inline-flex items-center gap-1'>
      <Badge variant={v}>{t.statusLabel[a.status] ?? a.status}</Badge>
      {a.exec_status !== 'none' && (
        <span
          className={
            a.exec_status === 'failed' || a.result_is_error
              ? 'text-xs text-destructive'
              : 'text-xs text-muted-foreground'
          }
        >
          {t.execLabel[a.exec_status]}
        </span>
      )}
    </span>
  )
}

// 审批中心（M1 第五块 PR5）：待我审批 / 我发起的；详情里通过（立即执行）、拒绝、撤回。
export function ApprovalsPage() {
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const [box, setBox] = useState<'todo' | 'mine'>('todo')
  const [status, setStatus] = useState('pending')
  const list = useQuery({
    queryKey: ['approvals', box, status],
    queryFn: () => approvalApi.list(box, status),
  })
  const open = search.id ?? null
  const setOpen = (id: number | null) =>
    navigate({ search: id ? { id } : {}, replace: true })
  const items = list.data?.items ?? []
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
        <div className='mb-3 flex items-center gap-3'>
          <Tabs value={box} onValueChange={(v) => setBox(v as 'todo' | 'mine')}>
            <TabsList>
              <TabsTrigger value='todo'>{t.todo}</TabsTrigger>
              <TabsTrigger value='mine'>{t.mine}</TabsTrigger>
            </TabsList>
          </Tabs>
          <NativeSelect
            value={status}
            onChange={setStatus}
            ariaLabel={t.status}
            options={[
              { value: '', label: t.all },
              ...Object.entries(t.statusLabel).map(([value, label]) => ({
                value,
                label,
              })),
            ]}
          />
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t.tool}</TableHead>
              <TableHead>{t.requester}</TableHead>
              <TableHead>{t.createdAt}</TableHead>
              <TableHead>{t.expiresAt}</TableHead>
              <TableHead>{t.status}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className='text-muted-foreground'>
                  {t.empty}
                </TableCell>
              </TableRow>
            )}
            {items.map((a) => (
              <TableRow
                key={a.id}
                className='cursor-pointer'
                onClick={() => setOpen(a.id)}
              >
                <TableCell>
                  <span className='me-2 font-medium'>{a.tool_name}</span>
                  <RiskBadge risk={a.risk_level} />
                </TableCell>
                <TableCell>{a.requester.name}</TableCell>
                <TableCell className='text-xs'>{fmt(a.created_at)}</TableCell>
                <TableCell className='text-xs'>{fmt(a.expires_at)}</TableCell>
                <TableCell>
                  <StatusBadge a={a} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {open != null && (
          <ApprovalDialog id={open} onClose={() => setOpen(null)} />
        )}
      </Main>
    </>
  )
}

function ApprovalDialog(props: { id: number; onClose: () => void }) {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['approval', props.id],
    queryFn: () => approvalApi.get(props.id),
    retry: false,
  })
  const [comment, setComment] = useState('')
  const done = () => {
    qc.invalidateQueries({ queryKey: ['approvals'] })
    qc.invalidateQueries({ queryKey: ['approval', props.id] })
    toast.success(t.done)
  }
  const onError = (e: Error) => toast.error(e.message)
  const approve = useMutation({
    mutationFn: () => approvalApi.approve(props.id, comment),
    onSuccess: done,
    onError,
  })
  const reject = useMutation({
    mutationFn: () => approvalApi.reject(props.id, comment),
    onSuccess: done,
    onError,
  })
  const withdraw = useMutation({
    mutationFn: () => approvalApi.withdraw(props.id),
    onSuccess: done,
    onError,
  })
  const a = q.data
  const busy = approve.isPending || reject.isPending || withdraw.isPending
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className='max-w-2xl'>
        <DialogHeader>
          <DialogTitle>
            {t.detail} #{props.id}
          </DialogTitle>
        </DialogHeader>
        {q.error && (
          <p className='text-sm text-destructive'>{q.error.message}</p>
        )}
        {a && (
          <div className='grid gap-3 text-sm'>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-medium'>{a.tool_name}</span>
              <RiskBadge risk={a.risk_level} />
              <StatusBadge a={a} />
            </div>
            <div className='grid grid-cols-2 gap-1 text-xs text-muted-foreground'>
              <span>
                {t.requester}：{a.requester.name}
              </span>
              <span>
                {t.approversLabel}：
                {a.approver_any_admin
                  ? zh.mcp.approversAny
                  : a.approvers.map((x) => x.name).join('、')}
              </span>
              <span>
                {t.createdAt}：{fmt(a.created_at)}
              </span>
              <span>
                {t.expiresAt}：{fmt(a.expires_at)}
              </span>
              {a.decided_by && (
                <span>
                  {t.decidedBy(a.decided_by.name)} · {fmt(a.decided_at)}
                </span>
              )}
              {a.trace_id && <span>trace：{a.trace_id}</span>}
            </div>
            <div>
              <p className='mb-1 font-medium'>{t.args}</p>
              <pre className='max-h-48 overflow-auto rounded bg-muted p-2 text-xs'>
                {JSON.stringify(a.args, null, 2)}
              </pre>
            </div>
            {a.context_summary && (
              <div>
                <p className='mb-1 font-medium'>{t.context}</p>
                <p className='rounded bg-muted p-2 text-xs whitespace-pre-wrap'>
                  {a.context_summary}
                </p>
              </div>
            )}
            {a.decision_comment && (
              <p className='text-xs'>
                {t.comment}：{a.decision_comment}
              </p>
            )}
            {a.result && (
              <div>
                <p className='mb-1 font-medium'>{t.result}</p>
                <pre
                  className={
                    'max-h-48 overflow-auto rounded bg-muted p-2 text-xs whitespace-pre-wrap' +
                    (a.result_is_error ? ' text-destructive' : '')
                  }
                >
                  {a.result}
                </pre>
              </div>
            )}
            {a.can_decide && (
              <Textarea
                rows={2}
                placeholder={t.comment}
                value={comment}
                onChange={(e) => setComment(e.target.value)}
              />
            )}
          </div>
        )}
        <DialogFooter>
          {a?.can_withdraw && (
            <Button
              variant='outline'
              disabled={busy}
              onClick={() => withdraw.mutate()}
            >
              {t.withdraw}
            </Button>
          )}
          {a?.can_decide && (
            <>
              <Button
                variant='outline'
                disabled={busy}
                onClick={() => reject.mutate()}
              >
                {t.reject}
              </Button>
              <Button disabled={busy} onClick={() => approve.mutate()}>
                {t.approve}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
