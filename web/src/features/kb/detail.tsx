import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import {
  ArrowLeft,
  RotateCw,
  Settings,
  Shield,
  Trash2,
  Upload,
} from 'lucide-react'
import { toast } from 'sonner'
import { kbApi, type KBFile } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'
import { AclEditor } from './acl-editor'
import { KBDialog } from './settings-dialog'

const t = zh.kb

function fmtSize(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

const busy = (s: KBFile['status']) =>
  s === 'pending' || s === 'parsing' || s === 'indexing'

// 知识库详情（M1 第四块 PR3）：文件列表 / 上传 / 文件权限 / 共享 / 设置。
export function KBDetailPage({ id }: { id: number }) {
  const qc = useQueryClient()
  const nav = useNavigate()
  const kb = useQuery({
    queryKey: ['kb', id],
    queryFn: () => kbApi.get(id),
    retry: false,
  })
  const files = useQuery({
    queryKey: ['kb', id, 'files'],
    queryFn: () => kbApi.files(id),
    enabled: kb.isSuccess,
    refetchInterval: (q) =>
      (q.state.data?.items ?? []).some((f) => busy(f.status)) ? 3000 : false,
  })
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [delFile, setDelFile] = useState<KBFile | null>(null)
  const [aclFile, setAclFile] = useState<KBFile | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['kb', id] })
  const onErr = (e: Error) => toast.error(e.message)

  const upload = async (list: FileList | null) => {
    for (const f of Array.from(list ?? [])) {
      const tid = toast.loading(t.uploading(f.name))
      try {
        const r = await kbApi.upload(id, f)
        if (r.duplicate) toast.info(t.duplicate(f.name), { id: tid })
        else toast.success(t.uploaded(f.name), { id: tid })
      } catch (e) {
        toast.error(t.uploadFailed(f.name, (e as Error).message), { id: tid })
      }
    }
    if (input.current) input.current.value = ''
    refresh()
  }
  const retry = useMutation({
    mutationFn: (fid: number) => kbApi.retryFile(id, fid),
    onSuccess: refresh,
    onError: onErr,
  })
  const rmFile = useMutation({
    mutationFn: (fid: number) => kbApi.removeFile(id, fid),
    onSuccess: () => {
      setDelFile(null)
      refresh()
    },
    onError: onErr,
  })
  const rmKB = useMutation({
    mutationFn: () => kbApi.remove(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['kbs'] })
      nav({ to: '/kbs' })
    },
    onError: onErr,
  })

  const head = (
    <Header>
      <div className='ms-auto flex items-center gap-2'>
        <ThemeSwitch />
        <ProfileDropdown />
      </div>
    </Header>
  )
  if (kb.isError)
    return (
      <>
        {head}
        <Main>
          <p className='text-sm text-muted-foreground'>{t.noAccess}</p>
          <Link to='/kbs' className='text-sm underline'>
            {t.back}
          </Link>
        </Main>
      </>
    )
  const k = kb.data
  const can = (p: string) => !!k?.perms.includes(p as never)
  return (
    <>
      {head}
      <Main>
        <Link
          to='/kbs'
          className='mb-2 inline-flex items-center gap-1 text-sm text-muted-foreground'
        >
          <ArrowLeft className='size-4' />
          {t.back}
        </Link>
        <div className='mb-4 flex flex-wrap items-center gap-2'>
          <div className='min-w-0'>
            <h1 className='truncate text-2xl font-bold tracking-tight'>
              {k?.name}
            </h1>
            <p className='text-sm text-muted-foreground'>{k?.description}</p>
          </div>
          <div className='ms-auto flex gap-2'>
            {can('edit') && (
              <>
                <input
                  ref={input}
                  type='file'
                  multiple
                  hidden
                  accept='.pdf,.docx,.xlsx,.pptx,.txt,.md,.markdown,.csv'
                  onChange={(e) => upload(e.target.files)}
                />
                <Button onClick={() => input.current?.click()}>
                  <Upload className='size-4' />
                  {t.upload}
                </Button>
              </>
            )}
            {can('manage') && (
              <>
                <Button variant='outline' onClick={() => setEditing(true)}>
                  <Settings className='size-4' />
                  {t.settings}
                </Button>
                <Button variant='outline' onClick={() => setRemoving(true)}>
                  <Trash2 className='size-4' />
                  {t.remove}
                </Button>
              </>
            )}
          </div>
        </div>
        {can('edit') && (
          <p className='mb-2 text-xs text-muted-foreground'>{t.uploadHint}</p>
        )}
        <Tabs defaultValue='files'>
          <TabsList>
            <TabsTrigger value='files'>
              {t.files(k?.file_count ?? 0)}
            </TabsTrigger>
            {can('manage') && (
              <TabsTrigger value='sharing'>{t.sharing}</TabsTrigger>
            )}
          </TabsList>
          <TabsContent value='files'>
            {(files.data?.items ?? []).length === 0 ? (
              <p className='p-3 text-sm text-muted-foreground'>{t.noFiles}</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t.fileName}</TableHead>
                    <TableHead>{t.size}</TableHead>
                    <TableHead>{t.status}</TableHead>
                    <TableHead>{t.chunks}</TableHead>
                    <TableHead>{t.updated}</TableHead>
                    <TableHead className='text-end'>{t.actions}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {files.data?.items.map((f) => (
                    <TableRow key={f.id}>
                      <TableCell className='max-w-72'>
                        <div className='truncate'>{f.name}</div>
                        {f.acl_custom && (
                          <Badge variant='outline' className='mt-1'>
                            {t.customBadge}
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell>{fmtSize(f.size)}</TableCell>
                      <TableCell>
                        <Badge
                          variant={
                            f.status === 'failed'
                              ? 'destructive'
                              : f.status === 'ready'
                                ? 'secondary'
                                : 'outline'
                          }
                          title={f.error}
                        >
                          {t.statusLabel[f.status]}
                        </Badge>
                        {f.status === 'failed' && f.error && (
                          <div className='mt-1 max-w-56 truncate text-xs text-destructive'>
                            {f.error}
                          </div>
                        )}
                      </TableCell>
                      <TableCell>{f.chunk_count}</TableCell>
                      <TableCell className='text-xs'>
                        {new Date(
                          f.indexed_at ?? f.created_at
                        ).toLocaleString()}
                      </TableCell>
                      <TableCell className='text-end'>
                        {can('edit') && f.status === 'failed' && (
                          <Button
                            size='sm'
                            variant='ghost'
                            title={t.retry}
                            onClick={() => retry.mutate(f.id)}
                          >
                            <RotateCw className='size-4' />
                          </Button>
                        )}
                        {can('manage') && (
                          <Button
                            size='sm'
                            variant='ghost'
                            title={t.fileACL}
                            onClick={() => setAclFile(f)}
                          >
                            <Shield className='size-4' />
                          </Button>
                        )}
                        {can('edit') && (
                          <Button
                            size='sm'
                            variant='ghost'
                            title={t.removeFile}
                            onClick={() => setDelFile(f)}
                          >
                            <Trash2 className='size-4' />
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </TabsContent>
          {can('manage') && (
            <TabsContent value='sharing'>
              <p className='mb-2 text-sm text-muted-foreground'>
                {t.sharingDesc}
              </p>
              <AclEditor
                queryKey={['kb', id, 'acl']}
                load={() => kbApi.acl(id)}
                grant={(b) => kbApi.grant(id, b)}
                revoke={(ty, sid) => kbApi.revoke(id, ty, sid)}
                perms={['view', 'edit', 'manage']}
              />
            </TabsContent>
          )}
        </Tabs>
      </Main>
      {editing && k && <KBDialog kb={k} onClose={() => setEditing(false)} />}
      <ConfirmDialog
        open={removing}
        onOpenChange={setRemoving}
        title={t.remove}
        desc={t.removeDesc}
        destructive
        isLoading={rmKB.isPending}
        handleConfirm={() => rmKB.mutate()}
      />
      <ConfirmDialog
        open={!!delFile}
        onOpenChange={(o) => !o && setDelFile(null)}
        title={t.removeFileTitle}
        desc={t.removeFileDesc(delFile?.name ?? '')}
        destructive
        isLoading={rmFile.isPending}
        handleConfirm={() => delFile && rmFile.mutate(delFile.id)}
      />
      {aclFile && (
        <FileACLDialog
          kbID={id}
          file={aclFile}
          onClose={() => setAclFile(null)}
        />
      )}
    </>
  )
}

function FileACLDialog(props: {
  kbID: number
  file: KBFile
  onClose: () => void
}) {
  const qc = useQueryClient()
  const { kbID, file } = props
  const [custom, setCustom] = useState(file.acl_custom)
  const mode = useMutation({
    mutationFn: (c: boolean) => kbApi.setACLMode(kbID, file.id, c),
    onSuccess: (f) => {
      setCustom(f.acl_custom)
      qc.invalidateQueries({ queryKey: ['kb', kbID, 'files'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className='max-w-2xl'>
        <DialogHeader>
          <DialogTitle>{t.fileACLTitle(file.name)}</DialogTitle>
        </DialogHeader>
        <label className='flex items-center gap-2 text-sm'>
          <Switch
            checked={custom}
            disabled={mode.isPending}
            onCheckedChange={(c) => mode.mutate(c)}
          />
          {custom ? t.custom : t.inherit}
        </label>
        {custom && (
          <AclEditor
            queryKey={['kb', kbID, 'file', file.id, 'acl']}
            load={() => kbApi.fileACL(kbID, file.id)}
            grant={(b) => kbApi.grantFile(kbID, file.id, b)}
            revoke={(ty, sid) => kbApi.revokeFile(kbID, file.id, ty, sid)}
            perms={['view']}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
