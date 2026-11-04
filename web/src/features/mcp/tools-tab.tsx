import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { X } from 'lucide-react'
import { toast } from 'sonner'
import {
  memberApi,
  toolApi,
  type AdminTool,
  type SubjectInfo,
  type ToolExecMode,
  type ToolRisk,
} from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import { Field, NativeSelect } from '@/features/models/fields'
import { ToolAclDialog } from './acl-dialog'

const t = zh.mcp
const riskVariant: Record<string, 'secondary' | 'outline' | 'destructive'> = {
  read_only: 'secondary',
  write: 'outline',
  high_risk: 'destructive',
}

export function RiskBadge({ risk }: { risk: string }) {
  return (
    <Badge variant={riskVariant[risk] ?? 'outline'}>
      {zh.toolRisk[risk] ?? risk}
    </Badge>
  )
}

export function ToolsTab() {
  const qc = useQueryClient()
  const [review, setReview] = useState(false)
  const list = useQuery({
    queryKey: ['admin-tools', review],
    queryFn: () => toolApi.list({ needs_review: review }),
  })
  const [editing, setEditing] = useState<AdminTool | null>(null)
  const [acl, setAcl] = useState<AdminTool | null>(null)
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['admin-tools'] })
    qc.invalidateQueries({ queryKey: ['mcp-servers'] })
  }
  const toggle = useMutation({
    mutationFn: (x: AdminTool) => toolApi.update(x.id, { enabled: !x.enabled }),
    onSuccess: refresh,
    onError: (e: Error) => toast.error(e.message),
  })
  const items = list.data?.items ?? []
  return (
    <div className='space-y-3'>
      <label className='flex items-center gap-2 text-sm'>
        <Switch checked={review} onCheckedChange={setReview} />
        {t.filterReview}
      </label>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t.tool}</TableHead>
            <TableHead>{t.server}</TableHead>
            <TableHead>{t.risk}</TableHead>
            <TableHead>{t.mode}</TableHead>
            <TableHead>{t.approvers}</TableHead>
            <TableHead>{t.acl}</TableHead>
            <TableHead>{t.enabled}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.length === 0 && (
            <TableRow>
              <TableCell colSpan={8} className='text-muted-foreground'>
                {t.toolsEmpty}
              </TableCell>
            </TableRow>
          )}
          {items.map((x) => (
            <TableRow key={x.id}>
              <TableCell>
                <div className='font-medium'>{x.name}</div>
                <div className='max-w-72 truncate text-xs text-muted-foreground'>
                  {x.description}
                </div>
                <div className='mt-1 flex gap-1'>
                  {x.needs_review && (
                    <Badge variant='destructive'>{t.needsReview}</Badge>
                  )}
                  {x.removed && <Badge variant='outline'>{t.removed}</Badge>}
                  {!x.server_enabled && (
                    <Badge variant='outline'>{t.serverOff}</Badge>
                  )}
                </div>
              </TableCell>
              <TableCell className='text-sm'>{x.server_name}</TableCell>
              <TableCell>
                <RiskBadge risk={x.risk_level} />
                {x.suggested_risk !== x.risk_level && (
                  <div className='text-xs text-muted-foreground'>
                    {t.suggested(zh.toolRisk[x.suggested_risk])}
                  </div>
                )}
              </TableCell>
              <TableCell className='text-sm'>
                {zh.toolMode[x.exec_mode] ?? x.exec_mode}
              </TableCell>
              <TableCell className='text-xs'>
                {x.exec_mode === 'require_approval'
                  ? x.approvers.length
                    ? x.approvers.map((a) => a.name).join('、')
                    : t.approversAny
                  : '—'}
              </TableCell>
              <TableCell>
                <Button size='sm' variant='ghost' onClick={() => setAcl(x)}>
                  {t.grants(x.grant_count)}
                </Button>
              </TableCell>
              <TableCell>
                <Switch
                  checked={x.enabled}
                  disabled={toggle.isPending || x.needs_review || x.removed}
                  onCheckedChange={() => toggle.mutate(x)}
                />
              </TableCell>
              <TableCell className='text-end'>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => setEditing(x)}
                >
                  {t.configure}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {editing && (
        <ToolDialog
          tool={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            refresh()
          }}
        />
      )}
      {acl && (
        <ToolAclDialog
          tool={acl}
          onClose={() => {
            setAcl(null)
            refresh()
          }}
        />
      )}
    </div>
  )
}

function ToolDialog(props: {
  tool: AdminTool
  onClose: () => void
  onSaved: () => void
}) {
  const x = props.tool
  const [risk, setRisk] = useState<ToolRisk>(x.risk_level)
  const [mode, setMode] = useState<ToolExecMode>(x.exec_mode)
  const [approvers, setApprovers] = useState<SubjectInfo[]>(x.approvers)
  const [timeout, setTimeoutSec] = useState(String(x.approval_timeout_sec))
  const [enabled, setEnabled] = useState(x.enabled)
  const [reviewed, setReviewed] = useState(false)
  const [q, setQ] = useState('')
  const members = useQuery({
    queryKey: ['members', 'approver-pick', q],
    queryFn: () => memberApi.list({ q }),
    enabled: q.trim().length > 0,
  })
  const save = useMutation({
    mutationFn: () =>
      toolApi.update(x.id, {
        risk_level: risk,
        exec_mode: mode,
        approver_ids: approvers.map((a) => a.id),
        approval_timeout_sec: Number(timeout),
        enabled,
        confirm_review: x.needs_review ? reviewed : undefined,
      }),
    onSuccess: props.onSaved,
    onError: (e: Error) => toast.error(e.message),
  })
  // 高风险工具不能自动执行（服务端同样校验）。
  const modes = (['require_approval', 'auto', 'deny'] as const).filter(
    (m) => !(m === 'auto' && risk === 'high_risk')
  )
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className='max-w-xl'>
        <DialogHeader>
          <DialogTitle>{x.name}</DialogTitle>
          <DialogDescription>{x.description}</DialogDescription>
        </DialogHeader>
        <div className='grid gap-3'>
          {x.needs_review && (
            <div className='rounded-md border border-destructive/50 p-2 text-sm'>
              <p className='text-destructive'>{t.needsReviewHint}</p>
              <label className='mt-1 flex items-center gap-2'>
                <Switch checked={reviewed} onCheckedChange={setReviewed} />
                {t.confirmReview}
              </label>
            </div>
          )}
          <details className='text-xs'>
            <summary className='cursor-pointer'>{t.schema}</summary>
            <pre className='mt-1 max-h-48 overflow-auto rounded bg-muted p-2'>
              {JSON.stringify(x.input_schema, null, 2)}
            </pre>
          </details>
          <div className='grid grid-cols-2 gap-3'>
            <Field label={t.risk}>
              <NativeSelect
                value={risk}
                onChange={(v) => {
                  setRisk(v as ToolRisk)
                  if (v === 'high_risk' && mode === 'auto')
                    setMode('require_approval')
                }}
                options={['read_only', 'write', 'high_risk'].map((r) => ({
                  value: r,
                  label: zh.toolRisk[r],
                }))}
              />
            </Field>
            <Field label={t.mode}>
              <NativeSelect
                value={mode}
                onChange={(v) => setMode(v as ToolExecMode)}
                options={modes.map((m) => ({
                  value: m,
                  label: zh.toolMode[m],
                }))}
              />
            </Field>
          </div>
          {mode === 'require_approval' && (
            <>
              <Field label={t.approvers}>
                <div className='flex flex-wrap gap-1'>
                  {approvers.length === 0 && (
                    <span className='text-xs text-muted-foreground'>
                      {t.approversAny}
                    </span>
                  )}
                  {approvers.map((a) => (
                    <Badge key={a.id} variant='secondary' className='gap-1'>
                      {a.name}
                      <button
                        aria-label='remove'
                        onClick={() =>
                          setApprovers(approvers.filter((b) => b.id !== a.id))
                        }
                      >
                        <X className='size-3' />
                      </button>
                    </Badge>
                  ))}
                </div>
                <Input
                  placeholder={zh.models.pickMember}
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                />
                {(members.data?.items ?? []).length > 0 && (
                  <ul className='max-h-32 overflow-y-auto rounded-md border text-sm'>
                    {(members.data?.items ?? [])
                      .filter((m) => !approvers.some((a) => a.id === m.id))
                      .map((m) => (
                        <li key={m.id}>
                          <button
                            className='w-full px-2 py-1 text-start hover:bg-accent'
                            onClick={() => {
                              setApprovers([
                                ...approvers,
                                { id: m.id, name: m.display_name || m.email },
                              ])
                              setQ('')
                            }}
                          >
                            {m.display_name} &lt;{m.email}&gt;
                          </button>
                        </li>
                      ))}
                  </ul>
                )}
              </Field>
              <Field label={t.approvalTimeout}>
                <Input
                  type='number'
                  min={60}
                  max={604800}
                  value={timeout}
                  onChange={(e) => setTimeoutSec(e.target.value)}
                />
              </Field>
            </>
          )}
          <label className='flex items-center gap-2 text-sm'>
            <Switch
              checked={enabled}
              disabled={x.removed}
              onCheckedChange={setEnabled}
            />
            {t.enabled}
          </label>
        </div>
        <DialogFooter>
          <Button disabled={save.isPending} onClick={() => save.mutate()}>
            {t.save}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
