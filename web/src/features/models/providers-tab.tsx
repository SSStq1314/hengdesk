import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { modelApi, type Provider, type ProviderInput } from '@/lib/api'
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
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Field, NativeSelect } from './fields'
import { TestButton } from './test-button'

type Edit = { id: number | null; v: ProviderInput }

export function ProvidersTab() {
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: ['providers'],
    queryFn: modelApi.providers,
  })
  const [edit, setEdit] = useState<Edit | null>(null)
  const [del, setDel] = useState<Provider | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['providers'] })
  const onErr = (e: Error) => toast.error(e.message)
  const save = useMutation({
    mutationFn: (e: Edit) => {
      const v = { ...e.v }
      if (!v.api_key) delete v.api_key
      return e.id
        ? modelApi.updateProvider(e.id, v)
        : modelApi.createProvider(v)
    },
    onSuccess: (p) => {
      setEdit(null)
      refresh()
      if (p?.egress && !p.egress.allowed)
        toast.warning(zh.egress.providerHint(p.egress.message ?? ''), {
          duration: 10000,
        })
    },
    onError: onErr,
  })
  const remove = useMutation({
    mutationFn: modelApi.removeProvider,
    onSuccess: () => {
      setDel(null)
      refresh()
    },
    onError: onErr,
  })
  const set = (patch: ProviderInput) =>
    setEdit((e) => (e ? { ...e, v: { ...e.v, ...patch } } : e))

  return (
    <>
      <div className='mb-3 flex'>
        <Button
          className='ms-auto'
          onClick={() =>
            setEdit({
              id: null,
              v: {
                kind: 'openai_compat',
                dialect: 'deepseek',
                enabled: true,
                timeout_seconds: 120,
              },
            })
          }
        >
          {zh.models.newProvider}
        </Button>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.models.name}</TableHead>
            <TableHead>{zh.models.dialect}</TableHead>
            <TableHead>{zh.models.baseURL}</TableHead>
            <TableHead>{zh.models.apiKey}</TableHead>
            <TableHead>{zh.models.status}</TableHead>
            <TableHead className='w-64'>{zh.common.actions}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(list.data?.items ?? []).map((p) => (
            <TableRow key={p.id}>
              <TableCell className='font-medium'>{p.name}</TableCell>
              <TableCell>
                {p.kind === 'anthropic' ? 'Anthropic' : p.dialect}
              </TableCell>
              <TableCell className='max-w-64 truncate'>{p.base_url}</TableCell>
              <TableCell>
                <Badge variant={p.has_api_key ? 'secondary' : 'outline'}>
                  {p.has_api_key ? zh.models.hasKey : zh.models.noKey}
                </Badge>
              </TableCell>
              <TableCell>
                {p.enabled ? zh.common.enabled : zh.common.disabled}
              </TableCell>
              <TableCell className='space-x-1'>
                <TestButton run={() => modelApi.testProvider(p.id)} />
                <Button
                  size='sm'
                  variant='ghost'
                  onClick={() =>
                    setEdit({
                      id: p.id,
                      v: {
                        name: p.name,
                        kind: p.kind,
                        dialect: p.dialect,
                        base_url: p.base_url,
                        enabled: p.enabled,
                        timeout_seconds: p.timeout_seconds,
                      },
                    })
                  }
                >
                  {zh.common.edit}
                </Button>
                <Button size='sm' variant='ghost' onClick={() => setDel(p)}>
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
              {edit?.id ? zh.models.editProvider : zh.models.newProvider}
            </DialogTitle>
          </DialogHeader>
          {edit && (
            <div className='grid gap-3'>
              <Field label={zh.models.name}>
                <Input
                  value={edit.v.name ?? ''}
                  onChange={(e) => set({ name: e.target.value })}
                />
              </Field>
              <Field label={zh.models.kind}>
                <NativeSelect
                  value={edit.v.kind ?? 'openai_compat'}
                  onChange={(v) => set({ kind: v })}
                  options={[
                    { value: 'openai_compat', label: zh.models.kindOpenAI },
                    { value: 'anthropic', label: zh.models.kindAnthropic },
                  ]}
                />
              </Field>
              {edit.v.kind !== 'anthropic' && (
                <Field label={zh.models.dialect}>
                  <NativeSelect
                    value={edit.v.dialect ?? 'generic'}
                    onChange={(v) => set({ dialect: v })}
                    options={[
                      { value: 'deepseek', label: 'DeepSeek' },
                      { value: 'dashscope', label: zh.models.dialectDashscope },
                      { value: 'vllm', label: 'vLLM' },
                      { value: 'generic', label: zh.models.dialectGeneric },
                    ]}
                  />
                </Field>
              )}
              {edit.v.kind !== 'anthropic' &&
                (edit.v.dialect === 'vllm' || edit.v.dialect === 'generic') && (
                  <p className='rounded-md border border-amber-300 bg-amber-50 p-2 text-xs text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200'>
                    {zh.models.internalHint}
                  </p>
                )}
              <Field label={zh.models.baseURL}>
                <Input
                  placeholder='https://api.deepseek.com'
                  value={edit.v.base_url ?? ''}
                  onChange={(e) => set({ base_url: e.target.value })}
                />
              </Field>
              <Field label={zh.models.apiKey}>
                <Input
                  type='password'
                  autoComplete='off'
                  placeholder={edit.id ? zh.models.apiKeyKeep : ''}
                  value={edit.v.api_key ?? ''}
                  onChange={(e) => set({ api_key: e.target.value })}
                />
              </Field>
              <Field label={zh.models.timeout}>
                <Input
                  type='number'
                  min={1}
                  value={edit.v.timeout_seconds ?? 120}
                  onChange={(e) =>
                    set({ timeout_seconds: Number(e.target.value) })
                  }
                />
              </Field>
              <label className='flex items-center gap-2 text-sm'>
                <Switch
                  checked={!!edit.v.enabled}
                  onCheckedChange={(c) => set({ enabled: c })}
                />
                {zh.common.enabled}
              </label>
            </div>
          )}
          <DialogFooter>
            <Button variant='outline' onClick={() => setEdit(null)}>
              {zh.common.cancel}
            </Button>
            <Button
              disabled={save.isPending}
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
        desc={zh.models.deleteConfirm(del?.name ?? '')}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => del && remove.mutate(del.id)}
      />
    </>
  )
}
