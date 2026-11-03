import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { deptApi, memberApi, modelApi, type Model } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { NativeSelect } from './fields'

const typeLabel: Record<string, string> = {
  user: zh.models.subjectUser,
  dept: zh.models.subjectDept,
  role: zh.models.subjectRole,
}

// 模型使用权限：按成员或部门（可含下级）授权，撤销即时生效。
export function AclDialog(props: { model: Model; onClose: () => void }) {
  const { model } = props
  const qc = useQueryClient()
  const key = ['model-acl', model.id]
  const acl = useQuery({ queryKey: key, queryFn: () => modelApi.acl(model.id) })
  const [type, setType] = useState<'user' | 'dept'>('dept')
  const [q, setQ] = useState('')
  const [sid, setSid] = useState('')
  const [children, setChildren] = useState(true)
  const depts = useQuery({ queryKey: ['departments'], queryFn: deptApi.list })
  const members = useQuery({
    queryKey: ['members', 'acl-pick', q],
    queryFn: () => memberApi.list({ q }),
    enabled: type === 'user' && q.trim().length > 0,
  })
  const onErr = (e: Error) => toast.error(e.message)
  const refresh = () => qc.invalidateQueries({ queryKey: key })
  const grant = useMutation({
    mutationFn: () =>
      modelApi.grant(model.id, {
        subject_type: type,
        subject_id: Number(sid),
        include_children: type === 'dept' && children,
      }),
    onSuccess: () => {
      setSid('')
      refresh()
    },
    onError: onErr,
  })
  const revoke = useMutation({
    mutationFn: (a: { t: string; id: number }) =>
      modelApi.revoke(model.id, a.t, a.id),
    onSuccess: refresh,
    onError: onErr,
  })
  const options =
    type === 'dept'
      ? (depts.data?.items ?? []).map((d) => ({
          value: String(d.id),
          label: d.name,
        }))
      : (members.data?.items ?? []).map((m) => ({
          value: String(m.id),
          label: `${m.display_name} <${m.email}>`,
        }))

  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className='max-w-xl'>
        <DialogHeader>
          <DialogTitle>{zh.models.aclTitle(model.name)}</DialogTitle>
          <DialogDescription>{zh.models.aclDesc}</DialogDescription>
        </DialogHeader>
        <ul className='divide-y rounded-md border'>
          {(acl.data?.items ?? []).length === 0 && (
            <li className='p-3 text-sm text-muted-foreground'>
              {zh.models.aclEmpty}
            </li>
          )}
          {(acl.data?.items ?? []).map((a) => (
            <li
              key={`${a.subject_type}-${a.subject_id}`}
              className='flex items-center gap-2 p-2 text-sm'
            >
              <Badge variant='outline'>{typeLabel[a.subject_type]}</Badge>
              <span>{a.subject_name || a.subject_id}</span>
              {a.include_children && (
                <span className='text-xs text-muted-foreground'>
                  {zh.models.includeChildren}
                </span>
              )}
              <Button
                size='sm'
                variant='ghost'
                className='ms-auto'
                disabled={revoke.isPending}
                onClick={() =>
                  revoke.mutate({ t: a.subject_type, id: a.subject_id })
                }
              >
                {zh.models.revoke}
              </Button>
            </li>
          ))}
        </ul>
        <div className='grid gap-2'>
          <div className='flex gap-2'>
            <NativeSelect
              value={type}
              onChange={(v) => {
                setType(v as 'user' | 'dept')
                setSid('')
              }}
              options={[
                { value: 'dept', label: zh.models.subjectDept },
                { value: 'user', label: zh.models.subjectUser },
              ]}
            />
            {type === 'user' && (
              <Input
                placeholder={zh.models.pickMember}
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            )}
          </div>
          <div className='flex items-center gap-2'>
            <NativeSelect
              value={sid}
              onChange={setSid}
              options={[{ value: '', label: '—' }, ...options]}
            />
            {type === 'dept' && (
              <label className='flex items-center gap-2 text-sm'>
                <Switch checked={children} onCheckedChange={setChildren} />
                {zh.models.includeChildren}
              </label>
            )}
            <Button
              className='ms-auto'
              disabled={!sid || grant.isPending}
              onClick={() => grant.mutate()}
            >
              {zh.models.grant}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
