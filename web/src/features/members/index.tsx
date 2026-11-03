import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
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
          <h1 className='me-auto text-2xl font-bold tracking-tight'>
            {zh.members.title}
          </h1>
          <Input
            className='w-56'
            placeholder={zh.members.searchPlaceholder}
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
              <option value=''>{zh.members.allDepartments}</option>
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
            {zh.common.query}
          </Button>
          {canManage && (
            <Button onClick={() => setDraft({ ...empty })}>
              {zh.members.create}
            </Button>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{zh.members.colName}</TableHead>
              <TableHead>{zh.members.colEmail}</TableHead>
              <TableHead>{zh.members.colEmployeeNo}</TableHead>
              <TableHead>{zh.members.colDepartment}</TableHead>
              <TableHead>{zh.members.colRoles}</TableHead>
              <TableHead>{zh.members.colStatus}</TableHead>
              <TableHead>{zh.members.colLastLogin}</TableHead>
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
                    <Badge variant='outline'>{zh.common.enabled}</Badge>
                  ) : (
                    <Badge variant='destructive'>{zh.common.disabled}</Badge>
                  )}
                  {!m.has_password && (
                    <Badge variant='outline' className='ms-1'>
                      {zh.members.noPassword}
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
                        <Button
                          size='sm'
                          variant='ghost'
                          aria-label={zh.common.actions}
                        >
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
                            {zh.common.edit}
                          </DropdownMenuItem>
                        )}
                        {canAssign && (
                          <DropdownMenuItem onClick={() => setRolesFor(m)}>
                            {zh.members.colRoles}
                          </DropdownMenuItem>
                        )}
                        {canManage && (
                          <>
                            <DropdownMenuItem
                              onClick={() => {
                                const pw = window.prompt(
                                  zh.members.resetPasswordPrompt(m.email)
                                )
                                if (pw)
                                  action.mutate({
                                    fn: () => memberApi.resetPassword(m.id, pw),
                                    msg: zh.members.passwordReset,
                                  })
                              }}
                            >
                              {zh.members.resetPassword}
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              onClick={() =>
                                action.mutate({
                                  fn: () => memberApi.unlock(m.id),
                                  msg: zh.members.unlocked,
                                })
                              }
                            >
                              {zh.members.unlock}
                            </DropdownMenuItem>
                            {m.status === 'active' ? (
                              <DropdownMenuItem
                                className='text-destructive'
                                onClick={() =>
                                  window.confirm(
                                    zh.members.confirmDisable(m.email)
                                  ) &&
                                  action.mutate({
                                    fn: () => memberApi.disable(m.id),
                                    msg: zh.members.disabledDone,
                                  })
                                }
                              >
                                {zh.common.disabled}
                              </DropdownMenuItem>
                            ) : (
                              <DropdownMenuItem
                                onClick={() =>
                                  action.mutate({
                                    fn: () => memberApi.enable(m.id),
                                    msg: zh.members.enabledDone,
                                  })
                                }
                              >
                                {zh.common.enabled}
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
            {zh.common.nextPage}
          </Button>
        )}
      </Main>

      <Dialog open={!!draft} onOpenChange={(o) => !o && setDraft(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {draft?.id ? zh.members.editTitle : zh.members.create}
            </DialogTitle>
          </DialogHeader>
          {draft && (
            <div className='grid gap-3'>
              <Label>{zh.members.colEmail}</Label>
              <Input
                disabled={!!draft.id}
                value={draft.email}
                onChange={(e) => setDraft({ ...draft, email: e.target.value })}
              />
              <Label>{zh.members.colName}</Label>
              <Input
                value={draft.display_name}
                onChange={(e) =>
                  setDraft({ ...draft, display_name: e.target.value })
                }
              />
              <Label>{zh.members.colEmployeeNo}</Label>
              <Input
                value={draft.employee_no}
                onChange={(e) =>
                  setDraft({ ...draft, employee_no: e.target.value })
                }
              />
              {!draft.id && (
                <>
                  <Label>{zh.members.initialPassword}</Label>
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
              <Label>{zh.members.colDepartment}</Label>
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
              {zh.common.save}
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
    return (
      <p className='text-sm text-muted-foreground'>
        {zh.members.noDepartments}
      </p>
    )
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
          <DialogTitle>{zh.members.rolesOf(member?.email ?? '')}</DialogTitle>
        </DialogHeader>
        <div className='flex flex-wrap gap-2'>
          {member?.roles.length === 0 && (
            <span className='text-sm text-muted-foreground'>
              {zh.members.onlyMember}
            </span>
          )}
          {member?.roles.map((r) => (
            <Badge key={`${r.key}-${r.department_id ?? 0}`} variant='secondary'>
              {r.name}
              {r.department_id
                ? `（${deptName(r.department_id) ?? r.department_id}）`
                : ''}
              <button
                className='ms-1'
                aria-label={zh.members.revoke}
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
            <option value=''>{zh.members.selectRole}</option>
            {roles.map((r) => (
              <option key={r.key} value={r.key}>
                {r.name}
                {r.privileged ? zh.members.privilegedSuffix : ''}
              </option>
            ))}
          </select>
          {selected?.dept_scoped && (
            <select
              className='h-9 rounded-md border border-input bg-background px-2 text-sm'
              value={dept ?? ''}
              onChange={(e) => setDept(Number(e.target.value) || undefined)}
            >
              <option value=''>{zh.members.selectDepartment}</option>
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
            {zh.members.assign}
          </Button>
        </div>
        <p className='text-xs text-muted-foreground'>{zh.members.rolesHelp}</p>
      </DialogContent>
    </Dialog>
  )
}
