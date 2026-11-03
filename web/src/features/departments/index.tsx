import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
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

const SAMPLE: string = zh.departments.csvTemplate

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
          <h1 className='me-auto text-2xl font-bold tracking-tight'>
            {zh.departments.title}
          </h1>
          {canManage && (
            <>
              <Button
                onClick={() => setEdit({ mode: 'create', id: null, name: '' })}
              >
                {zh.departments.newTop}
              </Button>
              <Button variant='outline' onClick={() => setImportOpen(true)}>
                {zh.departments.csvImport}
              </Button>
            </>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{zh.departments.colName}</TableHead>
              <TableHead className='w-24'>
                {zh.departments.colMembers}
              </TableHead>
              {canManage && (
                <TableHead className='w-64'>{zh.common.actions}</TableHead>
              )}
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
                      {zh.departments.addChild}
                    </Button>
                    <Button
                      size='sm'
                      variant='ghost'
                      onClick={() =>
                        setEdit({ mode: 'rename', id: d.id, name: d.name })
                      }
                    >
                      {zh.departments.rename}
                    </Button>
                    <Button
                      size='sm'
                      variant='ghost'
                      className='text-destructive'
                      onClick={() => {
                        if (
                          window.confirm(zh.departments.confirmDelete(d.path))
                        )
                          remove.mutate(d.id)
                      }}
                    >
                      {zh.common.delete}
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {nodes.length === 0 && !list.isLoading && (
              <TableRow>
                <TableCell colSpan={3} className='text-muted-foreground'>
                  {zh.departments.empty}
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
              {edit?.mode === 'rename'
                ? zh.departments.renameTitle
                : zh.departments.newTitle}
            </DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            placeholder={zh.departments.namePlaceholder}
            value={edit?.name ?? ''}
            onChange={(e) => edit && setEdit({ ...edit, name: e.target.value })}
          />
          <DialogFooter>
            <Button
              disabled={!edit?.name.trim() || save.isPending}
              onClick={() => edit && save.mutate(edit)}
            >
              {zh.common.save}
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
          zh.departments.importDone(r.created_users, r.updated_users)
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
          <DialogTitle>{zh.departments.importTitle}</DialogTitle>
          <DialogDescription>{zh.departments.importHelp}</DialogDescription>
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
              {zh.departments.importSummary(
                result.applied,
                result.rows,
                result.created_users,
                result.updated_users,
                result.new_departments.length
              )}
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
                  {zh.departments.importLineError(e.line, e.message)}
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
            {zh.departments.preview}
          </Button>
          <Button
            disabled={!canApply || run.isPending}
            onClick={() => run.mutate(false)}
          >
            {zh.departments.confirmImport}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
