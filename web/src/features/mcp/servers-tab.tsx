import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { mcpApi, type McpServer, type McpServerInput } from '@/lib/api'
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
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Field, NativeSelect } from '@/features/models/fields'

const t = zh.mcp

export function ServersTab() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['mcp-servers'], queryFn: mcpApi.list })
  const [editing, setEditing] = useState<McpServer | 'new' | null>(null)
  const [removing, setRemoving] = useState<McpServer | null>(null)
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['mcp-servers'] })
    qc.invalidateQueries({ queryKey: ['admin-tools'] })
  }
  const onErr = (e: Error) => toast.error(e.message)
  const discover = useMutation({
    mutationFn: (id: number) => mcpApi.discover(id),
    onSuccess: (r) => {
      toast.success(t.discovered(r.tools?.length ?? 0))
      refresh()
    },
    onError: (e: Error) => {
      onErr(e)
      refresh()
    },
  })
  const toggle = useMutation({
    mutationFn: (s: McpServer) => mcpApi.update(s.id, { enabled: !s.enabled }),
    onSuccess: refresh,
    onError: onErr,
  })
  const remove = useMutation({
    mutationFn: (id: number) => mcpApi.remove(id),
    onSuccess: () => {
      setRemoving(null)
      refresh()
    },
    onError: onErr,
  })
  const items = list.data?.items ?? []
  return (
    <div className='space-y-3'>
      <Button onClick={() => setEditing('new')}>{t.add}</Button>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t.name}</TableHead>
            <TableHead>{t.url}</TableHead>
            <TableHead>{t.status}</TableHead>
            <TableHead>{t.tools}</TableHead>
            <TableHead>{t.enabled}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.length === 0 && (
            <TableRow>
              <TableCell colSpan={6} className='text-muted-foreground'>
                {t.empty}
              </TableCell>
            </TableRow>
          )}
          {items.map((s) => (
            <TableRow key={s.id}>
              <TableCell>
                <div className='font-medium'>{s.name}</div>
                <div className='text-xs text-muted-foreground'>
                  {s.description}
                </div>
              </TableCell>
              <TableCell className='max-w-64 truncate text-xs'>
                {s.url}
                <div className='text-muted-foreground'>{s.transport}</div>
              </TableCell>
              <TableCell>
                {s.last_status ? (
                  <Badge
                    variant={
                      s.last_status === 'ok' ? 'secondary' : 'destructive'
                    }
                    title={s.last_error}
                  >
                    {s.last_status}
                  </Badge>
                ) : (
                  <span className='text-xs text-muted-foreground'>
                    {t.neverDiscovered}
                  </span>
                )}
                {s.last_error && (
                  <div className='max-w-48 truncate text-xs text-destructive'>
                    {s.last_error}
                  </div>
                )}
              </TableCell>
              <TableCell className='text-sm'>
                {t.toolsCount(s.tool_count, s.enabled_tools, s.review_tools)}
              </TableCell>
              <TableCell>
                <Switch
                  checked={s.enabled}
                  disabled={toggle.isPending}
                  onCheckedChange={() => toggle.mutate(s)}
                />
              </TableCell>
              <TableCell className='space-x-1 text-end whitespace-nowrap'>
                <Button
                  size='sm'
                  variant='outline'
                  disabled={discover.isPending}
                  onClick={() => discover.mutate(s.id)}
                >
                  {t.discover}
                </Button>
                <Button size='sm' variant='ghost' onClick={() => setEditing(s)}>
                  {t.edit}
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  className='text-destructive'
                  onClick={() => setRemoving(s)}
                >
                  {t.remove}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {editing && (
        <ServerDialog
          server={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            refresh()
          }}
        />
      )}
      <ConfirmDialog
        open={removing != null}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={t.removeTitle}
        desc={t.removeDesc(removing?.name ?? '')}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => removing && remove.mutate(removing.id)}
      />
    </div>
  )
}

// parseHeaders 解析「名称: 值」逐行输入；空输入返回 undefined（不改动已保存的头）。
function parseHeaders(raw: string): Record<string, string> | undefined {
  const lines = raw
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  if (lines.length === 0) return undefined
  const out: Record<string, string> = {}
  for (const l of lines) {
    const i = l.indexOf(':')
    if (i <= 0) continue
    out[l.slice(0, i).trim()] = l.slice(i + 1).trim()
  }
  return out
}

function ServerDialog(props: {
  server: McpServer | null
  onClose: () => void
  onSaved: () => void
}) {
  const s = props.server
  const [name, setName] = useState(s?.name ?? '')
  const [description, setDescription] = useState(s?.description ?? '')
  const [url, setUrl] = useState(s?.url ?? '')
  const [transport, setTransport] = useState<string>(
    s?.transport ?? 'streamable_http'
  )
  const [timeout, setTimeoutSec] = useState(String(s?.timeout_sec ?? 30))
  const [headers, setHeaders] = useState('')
  const [clear, setClear] = useState(false)
  const [enabled, setEnabled] = useState(s?.enabled ?? true)
  const save = useMutation({
    mutationFn: () => {
      const b: McpServerInput = {
        name: name.trim(),
        description: description.trim(),
        url: url.trim(),
        transport,
        timeout_sec: Number(timeout),
        enabled,
      }
      const h = parseHeaders(headers)
      if (h) b.headers = h
      else if (clear) b.headers = {}
      return s ? mcpApi.update(s.id, b) : mcpApi.create(b)
    },
    onSuccess: props.onSaved,
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className='max-w-lg'>
        <DialogHeader>
          <DialogTitle>{s ? t.edit : t.add}</DialogTitle>
        </DialogHeader>
        <div className='grid gap-3'>
          <Field label={t.name}>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label={t.description}>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
          <Field label={t.url}>
            <Input
              value={url}
              placeholder='https://mcp.example.com/mcp'
              onChange={(e) => setUrl(e.target.value)}
            />
          </Field>
          <div className='grid grid-cols-2 gap-3'>
            <Field label={t.transport}>
              <NativeSelect
                value={transport}
                onChange={setTransport}
                options={[
                  { value: 'streamable_http', label: 'Streamable HTTP' },
                  { value: 'sse', label: 'SSE' },
                ]}
              />
            </Field>
            <Field label={t.timeout}>
              <Input
                type='number'
                value={timeout}
                onChange={(e) => setTimeoutSec(e.target.value)}
              />
            </Field>
          </div>
          <Field label={t.headers}>
            <Textarea
              rows={3}
              value={headers}
              placeholder='Authorization: Bearer …'
              autoComplete='off'
              onChange={(e) => setHeaders(e.target.value)}
            />
          </Field>
          {s && s.header_names.length > 0 && (
            <div className='space-y-1 text-xs text-muted-foreground'>
              <p>{t.headersKept(s.header_names.join('、'))}</p>
              <label className='flex items-center gap-2'>
                <Switch checked={clear} onCheckedChange={setClear} />
                {t.clearHeaders}
              </label>
            </div>
          )}
          <label className='flex items-center gap-2 text-sm'>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
            {t.enabled}
          </label>
        </div>
        <DialogFooter>
          <Button
            disabled={!name.trim() || !url.trim() || save.isPending}
            onClick={() => save.mutate()}
          >
            {t.save}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
