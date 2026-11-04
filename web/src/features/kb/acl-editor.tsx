import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import {
  deptApi,
  memberApi,
  type KBACLInput,
  type KBACLRow,
  type KBPerm,
} from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { NativeSelect } from '@/features/models/fields'

const t = zh.kb
const typeLabel: Record<string, string> = {
  user: t.subjectUser,
  dept: t.subjectDept,
  role: t.subjectRole,
}

// AclEditor 知识库 / 文件共享列表与授权表单。perms 为可选的权限档（文件只有「查看」）。
export function AclEditor(props: {
  queryKey: unknown[]
  load: () => Promise<{ items: KBACLRow[] }>
  grant: (b: KBACLInput) => Promise<void>
  revoke: (type: string, sid: number) => Promise<void>
  perms: KBPerm[]
}) {
  const qc = useQueryClient()
  const acl = useQuery({ queryKey: props.queryKey, queryFn: props.load })
  const [type, setType] = useState<'user' | 'dept'>('user')
  const [q, setQ] = useState('')
  const [sid, setSid] = useState('')
  const [perm, setPerm] = useState<KBPerm>(props.perms[0])
  const [children, setChildren] = useState(true)
  const depts = useQuery({
    queryKey: ['departments'],
    queryFn: deptApi.list,
    enabled: type === 'dept',
    retry: false,
  })
  const members = useQuery({
    queryKey: ['members', 'kb-acl-pick', q],
    queryFn: () => memberApi.list({ q }),
    enabled: type === 'user' && q.trim().length > 0,
    retry: false,
  })
  const onErr = (e: Error) => toast.error(e.message)
  const refresh = () => qc.invalidateQueries({ queryKey: props.queryKey })
  const grant = useMutation({
    mutationFn: () =>
      props.grant({
        subject_type: type,
        subject_id: Number(sid),
        include_children: type === 'dept' && children,
        perm,
      }),
    onSuccess: () => {
      setSid('')
      refresh()
    },
    onError: onErr,
  })
  const revoke = useMutation({
    mutationFn: (a: { t: string; id: number }) => props.revoke(a.t, a.id),
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
  const rows = acl.data?.items ?? []
  return (
    <div className='grid gap-3'>
      <ul className='divide-y rounded-md border'>
        {rows.length === 0 && (
          <li className='p-3 text-sm text-muted-foreground'>{t.aclEmpty}</li>
        )}
        {rows.map((a) => (
          <li
            key={`${a.subject_type}-${a.subject_id}`}
            className='flex flex-wrap items-center gap-2 p-2 text-sm'
          >
            <Badge variant='outline'>{typeLabel[a.subject_type]}</Badge>
            <span>{a.subject_name || a.subject_id}</span>
            {a.include_children && (
              <span className='text-xs text-muted-foreground'>
                {t.includeChildren}
              </span>
            )}
            <span className='text-xs text-muted-foreground'>
              {a.perms.map((p) => t.perm[p]).join(' / ')}
            </span>
            <Button
              size='sm'
              variant='ghost'
              className='ms-auto'
              disabled={revoke.isPending}
              onClick={() =>
                revoke.mutate({ t: a.subject_type, id: a.subject_id })
              }
            >
              {t.revoke}
            </Button>
          </li>
        ))}
      </ul>
      <div className='flex flex-wrap gap-2'>
        <NativeSelect
          value={type}
          onChange={(v) => {
            setType(v as 'user' | 'dept')
            setSid('')
          }}
          options={[
            { value: 'user', label: t.subjectUser },
            { value: 'dept', label: t.subjectDept },
          ]}
        />
        {type === 'user' && (
          <Input
            className='w-56'
            placeholder={t.pickMember}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        )}
        <NativeSelect
          value={sid}
          onChange={setSid}
          options={[{ value: '', label: '—' }, ...options]}
        />
        {props.perms.length > 1 && (
          <NativeSelect
            value={perm}
            onChange={(v) => setPerm(v as KBPerm)}
            options={props.perms.map((p) => ({ value: p, label: t.perm[p] }))}
          />
        )}
        {type === 'dept' && (
          <label className='flex items-center gap-2 text-sm'>
            <Switch checked={children} onCheckedChange={setChildren} />
            {t.includeChildren}
          </label>
        )}
        <Button
          className='ms-auto'
          disabled={!sid || grant.isPending}
          onClick={() => grant.mutate()}
        >
          {t.grant}
        </Button>
      </div>
    </div>
  )
}
