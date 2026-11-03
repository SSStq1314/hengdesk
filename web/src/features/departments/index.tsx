import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import { deptApi, type ImportResult } from '@/lib/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { flattenTree } from './tree'

const SAMPLE =
  'dept_path,name,email,employee_no\n研发部/后端组,张三,zhangsan@example.com,E001\n'

type Edit = { mode: 'create' | 'rename'; id: number | null; name: string }

export function DepartmentsPage() {
  const perms = useAuthStore((s) => s.auth.user?.permissions ?? [])
  const canManage = perms.includes('org.dept.manage')
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['departments'], queryFn: deptApi.list })
  const nodes = flattenTree(list.data?.items ?? [])
  const [edit, setEdit] = useState<Edit | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const refresh = () => qc.invalidateQueries({ queryKey: ['departments'] })
  const onErr = (e: Error) => toast.error(e.message)

  const save = useMutation({
    mutationFn: (e: Edit) =>
      e.mode === 'create'
        ? deptApi.create(e.name, e.id)
        : deptApi.rename(e.id!, e.name),
    onSuccess: () => {
      setEdit(null)
      refresh()
    },
    onError: onErr,
  })
  const remove = useMutation({
    mutationFn: deptApi.remove,
    onSuccess: refresh,
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
          <h1 className='me-auto text-2xl font-bold tracking-tight'>部门</h1>
          {canManage && (
            <>
              <Button
                onClick={() => setEdit({ mode: 'create', id: null, name: '' })}
              >
                新建顶层部门
              </Button>
              <Button variant='outline' onClick={() => setImportOpen(true)}>
                CSV 导入
              </Button>
            </>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>名称</TableHead>
              <TableHead className='w-24'>成员数</TableHead>
              {canManage && <TableHead className='w-64'>操作</TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.map((d) => (
              <TableRow key={d.id}>
                <TableCell>
                  <span style={{ paddingInlineStart: `${d.depth * 1.25}rem` }}>
                    {d.depth > 0 && '└ '}
                    {d.name}
                  </span>
                </TableCell>
                <TableCell>{d.member_count}</TableCell>
                {canManage && (
                  <TableCell className='space-x-2'>
                    <Button
                      size='sm'
                      variant='ghost'
                      onClick={() =>
                        setEdit({ mode: 'create', id: d.id, name: '' })
                      }
                    >
                      添加子部门
                    </Button>
                    <Button
                      size='sm'
                      variant='ghost'
                      onClick={() =>
                        setEdit({ mode: 'rename', id: d.id, name: d.name })
                      }
                    >
                      重命名
                    </Button>
                    <Button
                      size='sm'
                      variant='ghost'
                      className='text-destructive'
                      onClick={() => {
                        if (window.confirm(`删除部门「${d.path}」？`))
                          remove.mutate(d.id)
                      }}
                    >
                      删除
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {nodes.length === 0 && !list.isLoading && (
              <TableRow>
                <TableCell colSpan={3} className='text-muted-foreground'>
                  还没有部门，可以新建或通过 CSV 导入。
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </Main>

      <Dialog open={!!edit} onOpenChange={(o) => !o && setEdit(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {edit?.mode === 'rename' ? '重命名部门' : '新建部门'}
            </DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            placeholder='部门名称'
            value={edit?.name ?? ''}
            onChange={(e) => edit && setEdit({ ...edit, name: e.target.value })}
          />
          <DialogFooter>
            <Button
              disabled={!edit?.name.trim() || save.isPending}
              onClick={() => edit && save.mutate(edit)}
            >
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ImportDialog
        open={importOpen}
        onOpenChange={setImportOpen}
        onDone={() => {
          refresh()
          qc.invalidateQueries({ queryKey: ['members'] })
        }}
      />
    </>
  )
}

function ImportDialog({
  open,
  onOpenChange,
  onDone,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  onDone: () => void
}) {
  const [csv, setCsv] = useState(SAMPLE)
  const [result, setResult] = useState<ImportResult | null>(null)
  const run = useMutation({
    mutationFn: (dry: boolean) => deptApi.importCsv(csv, dry),
    onSuccess: (r) => {
      setResult(r)
      if (r.applied) {
        toast.success(
          `导入完成：新建 ${r.created_users} 人，更新 ${r.updated_users} 人`
        )
        onDone()
      }
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const canApply = result?.dry_run && result.errors.length === 0

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o)
        if (!o) setResult(null)
      }}
    >
      <DialogContent className='sm:max-w-2xl'>
        <DialogHeader>
          <DialogTitle>CSV 导入部门与成员</DialogTitle>
          <DialogDescription>
            表头：dept_path,name,email,employee_no。dept_path 用 /
            分隔层级，缺失的部门会自动创建；成员按邮箱新建或更新，新建的成员没有本地密码，需重置密码或通过
            SSO 登录。先预览，确认无误后再导入；有任何错误时整批不写入。
          </DialogDescription>
        </DialogHeader>
        <Input
          type='file'
          accept='.csv,text/csv'
          onChange={async (e) => {
            const f = e.target.files?.[0]
            if (f) {
              setCsv(await f.text())
              setResult(null)
            }
          }}
        />
        <Textarea
          className='h-40 font-mono text-xs'
          value={csv}
          onChange={(e) => {
            setCsv(e.target.value)
            setResult(null)
          }}
        />
        {result && (
          <Alert variant={result.errors.length ? 'destructive' : 'default'}>
            <AlertTitle>
              {result.applied ? '已导入' : '预览'}：共 {result.rows} 行，新建{' '}
              {result.created_users} 人，更新 {result.updated_users}{' '}
              人，新建部门 {result.new_departments.length} 个
            </AlertTitle>
            <AlertDescription>
              {result.new_departments.length > 0 && (
                <div className='flex flex-wrap gap-1'>
                  {result.new_departments.slice(0, 30).map((p) => (
                    <Badge key={p} variant='secondary'>
                      {p}
                    </Badge>
                  ))}
                </div>
              )}
              {result.errors.slice(0, 50).map((e) => (
                <div key={`${e.line}-${e.message}`}>
                  第 {e.line} 行：{e.message}
                </div>
              ))}
            </AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button
            variant='outline'
            disabled={run.isPending}
            onClick={() => run.mutate(true)}
          >
            预览
          </Button>
          <Button
            disabled={!canApply || run.isPending}
            onClick={() => run.mutate(false)}
          >
            确认导入
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
