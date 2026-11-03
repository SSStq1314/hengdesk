import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import {
  deptApi,
  memberApi,
  roleApi,
  type Member,
  type MemberFilter,
} from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { flattenTree, type DeptNode } from '../departments/tree'

type Draft = {
  id?: number
  email: string
  display_name: string
  employee_no: string
  department_ids: number[]
  password: string
}

const empty: Draft = {
  email: '',
  display_name: '',
  employee_no: '',
  department_ids: [],
  password: '',
}

export function MembersPage() {
  const perms = useAuthStore((s) => s.auth.user?.permissions ?? [])
  const canManage = perms.includes('org.member.manage')
  const canAssign = perms.includes('org.role.assign')
  const qc = useQueryClient()
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<MemberFilter>({})
  const list = useQuery({
    queryKey: ['members', filter],
    queryFn: () => memberApi.list(filter),
  })
  const depts = useQuery({
    queryKey: ['departments'],
    queryFn: deptApi.list,
    enabled: perms.includes('org.dept.read'),
  })
  const nodes = flattenTree(depts.data?.items ?? [])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [rolesFor, setRolesFor] = useState<Member | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['members'] })
  const onErr = (e: Error) => toast.error(e.message)

  const save = useMutation({
    mutationFn: (d: Draft) => {
      const body = {
        display_name: d.display_name,
        employee_no: d.employee_no,
        department_ids: d.department_ids,
      }
      return d.id
        ? memberApi.update(d.id, body)
        : memberApi.create({
            ...body,
            email: d.email,
            password: d.password || undefined,
          })
    },
    onSuccess: () => {
      setDraft(null)
      refresh()
    },
    onError: onErr,
  })
  const action = useMutation({
    mutationFn: async ({
      fn,
      msg,
    }: {
      fn: () => Promise<void>
      msg: string
    }) => {
      await fn()
      return msg
    },
    onSuccess: (msg) => {
      toast.success(msg)
      refresh()
    },
    onError: onErr,
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
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <h1 className='me-auto text-2xl font-bold tracking-tight'>成员</h1>
          <Input
            className='w-56'
            placeholder='邮箱 / 姓名 / 工号'
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) =>
              e.key === 'Enter' &&
              setFilter({ ...filter, q, before_id: undefined })
            }
          />
          {nodes.length > 0 && (
            <select
              className='h-9 rounded-md border border-input bg-background px-2 text-sm'
              value={filter.dept_id ?? ''}
              onChange={(e) =>
                setFilter({
                  ...filter,
                  dept_id: Number(e.target.value) || undefined,
                  before_id: undefined,
                })
              }
            >
              <option value=''>全部部门</option>
              {nodes.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.path}
                </option>
              ))}
            </select>
          )}
          <Button
            variant='outline'
            onClick={() => setFilter({ ...filter, q, before_id: undefined })}
          >
            查询
          </Button>
          {canManage && (
            <Button onClick={() => setDraft({ ...empty })}>新建成员</Button>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>姓名</TableHead>
              <TableHead>邮箱</TableHead>
              <TableHead>工号</TableHead>
              <TableHead>部门</TableHead>
              <TableHead>角色</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>最近登录</TableHead>
              <TableHead className='w-16' />
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.data?.items.map((m) => (
              <TableRow key={m.id}>
                <TableCell>{m.display_name || '—'}</TableCell>
                <TableCell>{m.email}</TableCell>
                <TableCell>{m.employee_no || '—'}</TableCell>
                <TableCell>
                  {m.departments.map((d) => d.name).join('、') || '—'}
                </TableCell>
                <TableCell className='space-x-1'>
                  {m.roles.map((r) => (
                    <Badge
                      key={`${r.key}-${r.department_id ?? 0}`}
                      variant='secondary'
                    >
                      {r.name}
                    </Badge>
                  ))}
                </TableCell>
                <TableCell>
                  {m.status === 'active' ? (
                    <Badge variant='outline'>启用</Badge>
                  ) : (
                    <Badge variant='destructive'>停用</Badge>
                  )}
                  {!m.has_password && (
                    <Badge variant='outline' className='ms-1'>
                      未设密码
                    </Badge>
                  )}
                </TableCell>
                <TableCell>
                  {m.last_login_at
                    ? new Date(m.last_login_at).toLocaleString('zh-CN')
                    : '—'}
                </TableCell>
                <TableCell>
                  {(canManage || canAssign) && (
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button size='sm' variant='ghost' aria-label='操作'>
                          ⋯
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align='end'>
                        {canManage && (
                          <DropdownMenuItem
                            onClick={() =>
                              setDraft({
                                id: m.id,
                                email: m.email,
                                display_name: m.display_name,
                                employee_no: m.employee_no,
                                department_ids: m.departments.map((d) => d.id),
                                password: '',
                              })
                            }
                          >
                            编辑
                          </DropdownMenuItem>
                        )}
                        {canAssign && (
                          <DropdownMenuItem onClick={() => setRolesFor(m)}>
                            角色
                          </DropdownMenuItem>
                        )}
                        {canManage && (
                          <>
                            <DropdownMenuItem
                              onClick={() => {
                                const pw = window.prompt(
                                  `为 ${m.email} 设置新密码（至少 8 位，对方所有会话将下线）`
                                )
                                if (pw)
                                  action.mutate({
                                    fn: () => memberApi.resetPassword(m.id, pw),
                                    msg: '密码已重置',
                                  })
                              }}
                            >
                              重置密码
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              onClick={() =>
                                action.mutate({
                                  fn: () => memberApi.unlock(m.id),
                                  msg: '已解除登录锁定',
                                })
                              }
                            >
                              解除锁定
                            </DropdownMenuItem>
                            {m.status === 'active' ? (
                              <DropdownMenuItem
                                className='text-destructive'
                                onClick={() =>
                                  window.confirm(
                                    `停用 ${m.email}？对方会立即下线。`
                                  ) &&
                                  action.mutate({
                                    fn: () => memberApi.disable(m.id),
                                    msg: '已停用',
                                  })
                                }
                              >
                                停用
                              </DropdownMenuItem>
                            ) : (
                              <DropdownMenuItem
                                onClick={() =>
                                  action.mutate({
                                    fn: () => memberApi.enable(m.id),
                                    msg: '已启用',
                                  })
                                }
                              >
                                启用
                              </DropdownMenuItem>
                            )}
                          </>
                        )}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {!!list.data?.next_before_id && (
          <Button
            variant='outline'
            className='mt-4'
            onClick={() =>
              setFilter({ ...filter, before_id: list.data.next_before_id })
            }
          >
            下一页
          </Button>
        )}
      </Main>

      <Dialog open={!!draft} onOpenChange={(o) => !o && setDraft(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{draft?.id ? '编辑成员' : '新建成员'}</DialogTitle>
          </DialogHeader>
          {draft && (
            <div className='grid gap-3'>
              <Label>邮箱</Label>
              <Input
                disabled={!!draft.id}
                value={draft.email}
                onChange={(e) => setDraft({ ...draft, email: e.target.value })}
              />
              <Label>姓名</Label>
              <Input
                value={draft.display_name}
                onChange={(e) =>
                  setDraft({ ...draft, display_name: e.target.value })
                }
              />
              <Label>工号</Label>
              <Input
                value={draft.employee_no}
                onChange={(e) =>
                  setDraft({ ...draft, employee_no: e.target.value })
                }
              />
              {!draft.id && (
                <>
                  <Label>
                    初始密码（可选，留空则需重置密码或通过 SSO 登录）
                  </Label>
                  <Input
                    type='password'
                    autoComplete='new-password'
                    value={draft.password}
                    onChange={(e) =>
                      setDraft({ ...draft, password: e.target.value })
                    }
                  />
                </>
              )}
              <Label>部门</Label>
              <DeptPicker
                nodes={nodes}
                value={draft.department_ids}
                onChange={(ids) => setDraft({ ...draft, department_ids: ids })}
              />
            </div>
          )}
          <DialogFooter>
            <Button
              disabled={save.isPending || !draft?.email}
              onClick={() => draft && save.mutate(draft)}
            >
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <RolesDialog
        member={rolesFor}
        nodes={nodes}
        onClose={() => setRolesFor(null)}
        onChanged={(m) => {
          refresh()
          setRolesFor(m)
        }}
      />
    </>
  )
}

function DeptPicker({
  nodes,
  value,
  onChange,
}: {
  nodes: DeptNode[]
  value: number[]
  onChange: (ids: number[]) => void
}) {
  if (nodes.length === 0)
    return <p className='text-sm text-muted-foreground'>暂无部门</p>
  return (
    <ScrollArea className='h-40 rounded-md border p-2'>
      {nodes.map((d) => (
        <label
          key={d.id}
          className='flex items-center gap-2 py-1 text-sm'
          style={{ paddingInlineStart: `${d.depth}rem` }}
        >
          <Checkbox
            checked={value.includes(d.id)}
            onCheckedChange={(c) =>
              onChange(c ? [...value, d.id] : value.filter((x) => x !== d.id))
            }
          />
          {d.name}
        </label>
      ))}
    </ScrollArea>
  )
}

function RolesDialog({
  member,
  nodes,
  onClose,
  onChanged,
}: {
  member: Member | null
  nodes: DeptNode[]
  onClose: () => void
  onChanged: (m: Member | null) => void
}) {
  const matrix = useQuery({ queryKey: ['roles'], queryFn: roleApi.matrix })
  const [role, setRole] = useState('')
  const [dept, setDept] = useState<number | undefined>()
  const roles = (matrix.data?.roles ?? []).filter((r) => r.key !== 'member')
  const selected = roles.find((r) => r.key === role)
  const change = useMutation({
    mutationFn: async (fn: () => Promise<void>) => {
      await fn()
      const res = await memberApi.list({ q: member!.email })
      return res.items.find((x) => x.id === member!.id) ?? null
    },
    onSuccess: onChanged,
    onError: (e: Error) => toast.error(e.message),
  })
  const deptName = (id?: number) => nodes.find((d) => d.id === id)?.path
  return (
    <Dialog open={!!member} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{member?.email} 的角色</DialogTitle>
        </DialogHeader>
        <div className='flex flex-wrap gap-2'>
          {member?.roles.length === 0 && (
            <span className='text-sm text-muted-foreground'>仅普通成员</span>
          )}
          {member?.roles.map((r) => (
            <Badge key={`${r.key}-${r.department_id ?? 0}`} variant='secondary'>
              {r.name}
              {r.department_id
                ? `（${deptName(r.department_id) ?? r.department_id}）`
                : ''}
              <button
                className='ms-1'
                aria-label='撤销'
                onClick={() =>
                  change.mutate(() =>
                    memberApi.revokeRole(member.id, r.key, r.department_id)
                  )
                }
              >
                ×
              </button>
            </Badge>
          ))}
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <select
            className='h-9 rounded-md border border-input bg-background px-2 text-sm'
            value={role}
            onChange={(e) => setRole(e.target.value)}
          >
            <option value=''>选择角色</option>
            {roles.map((r) => (
              <option key={r.key} value={r.key}>
                {r.name}
                {r.privileged ? '（特权）' : ''}
              </option>
            ))}
          </select>
          {selected?.dept_scoped && (
            <select
              className='h-9 rounded-md border border-input bg-background px-2 text-sm'
              value={dept ?? ''}
              onChange={(e) => setDept(Number(e.target.value) || undefined)}
            >
              <option value=''>选择部门</option>
              {nodes.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.path}
                </option>
              ))}
            </select>
          )}
          <Button
            disabled={
              !role || (selected?.dept_scoped && !dept) || change.isPending
            }
            onClick={() =>
              change.mutate(() =>
                memberApi.assignRole(
                  member!.id,
                  role,
                  selected?.dept_scoped ? dept : undefined
                )
              )
            }
          >
            分配
          </Button>
        </div>
        <p className='text-xs text-muted-foreground'>
          超级管理员与安全审计员互斥（三员分立）；分配特权角色需要相应权限，所有变更都会记入审计日志。
        </p>
      </DialogContent>
    </Dialog>
  )
}
