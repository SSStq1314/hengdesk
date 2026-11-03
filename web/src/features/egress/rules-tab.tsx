import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import {
  egressApi,
  type EgressHint,
  type EgressRule,
  type EgressRuleInput,
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Field, NativeSelect } from '@/features/models/fields'

const kinds = [
  { value: 'host', label: zh.egress.kindHost },
  { value: 'wildcard', label: zh.egress.kindWildcard },
  { value: 'ip', label: zh.egress.kindIp },
  { value: 'cidr', label: zh.egress.kindCidr },
]
const kindLabel = Object.fromEntries(kinds.map((k) => [k.value, k.label]))

type Edit = { id: number | null; v: EgressRuleInput }

export function RulesTab() {
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: ['egress-rules'],
    queryFn: egressApi.rules,
  })
  const [edit, setEdit] = useState<Edit | null>(null)
  const [del, setDel] = useState<EgressRule | null>(null)
  const [url, setUrl] = useState('')
  const [hint, setHint] = useState<EgressHint | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['egress-rules'] })
  const onErr = (e: Error) => toast.error(e.message)
  const save = useMutation({
    mutationFn: (e: Edit) =>
      e.id ? egressApi.update(e.id, e.v) : egressApi.create(e.v),
    onSuccess: (r) => {
      setEdit(null)
      refresh()
      if (r.private) toast.warning(zh.egress.privateWarn)
      if (r.any_port) toast.warning(zh.egress.anyPortSaved)
    },
    onError: onErr,
  })
  const remove = useMutation({
    mutationFn: egressApi.remove,
    onSuccess: () => {
      setDel(null)
      refresh()
    },
    onError: onErr,
  })
  const review = useMutation({
    mutationFn: egressApi.review,
    onSuccess: () => {
      toast.success(zh.egress.reviewed)
      refresh()
    },
    onError: onErr,
  })
  const check = useMutation({
    mutationFn: egressApi.check,
    onSuccess: setHint,
    onError: onErr,
  })
  const set = (patch: Partial<EgressRuleInput>) =>
    setEdit((e) => (e ? { ...e, v: { ...e.v, ...patch } } : e))
  const items = list.data?.items ?? []
  const pending = list.data?.pending_review ?? 0
  const sourceLabel: Record<string, string> = {
    manual: zh.egress.sourceManual,
    upgrade: zh.egress.sourceSeed,
    oidc: zh.egress.sourceOidc,
  }

  return (
    <>
      <p className='mb-3 text-sm text-muted-foreground'>
        {zh.egress.defaultDeny}
      </p>
      {pending > 0 && (
        <div
          role='alert'
          className='mb-3 rounded-md border border-amber-500/50 bg-amber-500/10 p-3 text-sm'
        >
          {zh.egress.reviewBanner(pending)}
        </div>
      )}
      <div className='mb-3 flex flex-wrap items-center gap-2'>
        <Input
          className='w-96'
          placeholder={zh.egress.checkPlaceholder}
          value={url}
          onChange={(e) => {
            setUrl(e.target.value)
            setHint(null)
          }}
        />
        <Button
          variant='outline'
          disabled={!url || check.isPending}
          onClick={() => check.mutate(url)}
        >
          {zh.egress.check}
        </Button>
        {hint &&
          (hint.allowed ? (
            <Badge>{zh.egress.allowed}</Badge>
          ) : (
            <Badge variant='destructive'>
              {zh.egress.denied(hint.message ?? '')}
            </Badge>
          ))}
        <Button
          className='ms-auto'
          onClick={() =>
            setEdit({
              id: null,
              v: { kind: 'host', value: '', ports: '', note: '' },
            })
          }
        >
          {zh.egress.add}
        </Button>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.egress.kind}</TableHead>
            <TableHead>{zh.egress.value}</TableHead>
            <TableHead>{zh.egress.ports}</TableHead>
            <TableHead>{zh.egress.source}</TableHead>
            <TableHead>{zh.egress.note}</TableHead>
            <TableHead>{zh.common.actions}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.isSuccess && items.length === 0 && (
            <TableRow>
              <TableCell colSpan={6} className='text-muted-foreground'>
                {zh.egress.empty}
              </TableCell>
            </TableRow>
          )}
          {items.map((r) => (
            <TableRow key={r.id}>
              <TableCell>{kindLabel[r.kind] ?? r.kind}</TableCell>
              <TableCell className='font-mono'>
                {r.value}{' '}
                {r.private && (
                  <Badge variant='destructive'>{zh.egress.privateBadge}</Badge>
                )}
              </TableCell>
              <TableCell>
                {r.any_port ? (
                  <Badge variant='destructive' title={zh.egress.anyPortWarn}>
                    {zh.egress.anyPort}
                  </Badge>
                ) : (
                  r.ports || zh.egress.portsDefault
                )}
              </TableCell>
              <TableCell>
                {sourceLabel[r.source] ?? r.source}{' '}
                {r.needs_review && (
                  <Badge variant='outline'>{zh.egress.needsReview}</Badge>
                )}
              </TableCell>
              <TableCell>{r.note}</TableCell>
              <TableCell className='space-x-2'>
                {r.needs_review && (
                  <Button
                    size='sm'
                    disabled={review.isPending}
                    onClick={() => review.mutate(r.id)}
                  >
                    {zh.egress.markReviewed}
                  </Button>
                )}
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() =>
                    setEdit({
                      id: r.id,
                      v: {
                        kind: r.kind,
                        value: r.value,
                        ports: r.ports,
                        note: r.note,
                      },
                    })
                  }
                >
                  {zh.common.edit}
                </Button>
                <Button
                  size='sm'
                  variant='destructive'
                  onClick={() => setDel(r)}
                >
                  {zh.common.delete}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      <Dialog open={!!edit} onOpenChange={(o) => !o && setEdit(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {edit?.id ? zh.egress.editTitle : zh.egress.createTitle}
            </DialogTitle>
          </DialogHeader>
          {edit && (
            <div className='grid gap-3'>
              <Field label={zh.egress.kind}>
                <NativeSelect
                  ariaLabel={zh.egress.kind}
                  value={edit.v.kind}
                  onChange={(v) => set({ kind: v as EgressRuleInput['kind'] })}
                  options={kinds}
                />
              </Field>
              <Field label={zh.egress.value}>
                <Input
                  placeholder={zh.egress.valuePlaceholder}
                  value={edit.v.value}
                  onChange={(e) => set({ value: e.target.value })}
                />
              </Field>
              <Field label={zh.egress.ports}>
                <Input
                  placeholder={zh.egress.portsPlaceholder}
                  value={edit.v.ports}
                  onChange={(e) => set({ ports: e.target.value })}
                />
                <p
                  className={
                    edit.v.ports.trim() === '*'
                      ? 'mt-1 text-xs text-destructive'
                      : 'mt-1 text-xs text-muted-foreground'
                  }
                >
                  {edit.v.ports.trim() === '*'
                    ? zh.egress.anyPortWarn
                    : zh.egress.portsHint}
                </p>
              </Field>
              <Field label={zh.egress.note}>
                <Input
                  value={edit.v.note}
                  onChange={(e) => set({ note: e.target.value })}
                />
              </Field>
            </div>
          )}
          <DialogFooter>
            <Button variant='outline' onClick={() => setEdit(null)}>
              {zh.common.cancel}
            </Button>
            <Button
              disabled={!edit?.v.value || save.isPending}
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
        desc={zh.egress.deleteConfirm(del?.value ?? '')}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => del && remove.mutate(del.id)}
      />
    </>
  )
}
