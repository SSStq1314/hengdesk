import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { modelApi, type Model, type ModelInput } from '@/lib/api'
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
import { AclDialog } from './acl-dialog'
import { Field, NativeSelect } from './fields'
import { toMicros, yuan } from './format'
import { TestButton } from './test-button'

// 编辑态：价格以「元 / 百万 token」字符串输入，保存时换算为微元。
type Form = Omit<
  ModelInput,
  'price_input_micros' | 'price_cached_micros' | 'price_output_micros'
> & { pin: string; pcached: string; pout: string }
type Edit = { id: number | null; v: Form }

const toForm = (m: Model): Form => ({
  name: m.name,
  provider_id: m.provider_id,
  upstream_model: m.upstream_model,
  thinking: m.thinking,
  tool_choice_policy: m.tool_choice_policy,
  context_window: m.context_window,
  max_output_tokens: m.max_output_tokens,
  enabled: m.enabled,
  pin: yuan(m.price_input_micros),
  pcached: yuan(m.price_cached_micros),
  pout: yuan(m.price_output_micros),
})

export function ModelsTab() {
  const qc = useQueryClient()
  const models = useQuery({ queryKey: ['models'], queryFn: modelApi.models })
  const providers = useQuery({
    queryKey: ['providers'],
    queryFn: modelApi.providers,
  })
  const provName = new Map(
    (providers.data?.items ?? []).map((p) => [p.id, p.name])
  )
  const [edit, setEdit] = useState<Edit | null>(null)
  const [del, setDel] = useState<Model | null>(null)
  const [acl, setAcl] = useState<Model | null>(null)
  const refresh = () => qc.invalidateQueries({ queryKey: ['models'] })
  const onErr = (e: Error) => toast.error(e.message)

  const save = useMutation({
    mutationFn: ({ id, v }: Edit) => {
      const { pin, pcached, pout, ...rest } = v
      const body: ModelInput = {
        ...rest,
        price_input_micros: toMicros(pin),
        price_cached_micros: toMicros(pcached),
        price_output_micros: toMicros(pout),
      }
      return id ? modelApi.updateModel(id, body) : modelApi.createModel(body)
    },
    onSuccess: () => {
      setEdit(null)
      refresh()
    },
    onError: onErr,
  })
  const setDefault = useMutation({
    mutationFn: modelApi.setDefault,
    onSuccess: refresh,
    onError: onErr,
  })
  const remove = useMutation({
    mutationFn: modelApi.removeModel,
    onSuccess: () => {
      setDel(null)
      refresh()
    },
    onError: onErr,
  })
  const set = (patch: Partial<Form>) =>
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
                provider_id: providers.data?.items[0]?.id,
                thinking: 'off',
                tool_choice_policy: 'downgrade',
                enabled: true,
                pin: '0',
                pcached: '0',
                pout: '0',
              },
            })
          }
        >
          {zh.models.newModel}
        </Button>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{zh.models.name}</TableHead>
            <TableHead>{zh.models.provider}</TableHead>
            <TableHead>{zh.models.upstreamModel}</TableHead>
            <TableHead>{zh.models.price}</TableHead>
            <TableHead>{zh.models.status}</TableHead>
            <TableHead className='w-96'>{zh.common.actions}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(models.data?.items ?? []).map((m) => (
            <TableRow key={m.id}>
              <TableCell className='font-medium'>
                {m.name} {m.is_default && <Badge>{zh.models.isDefault}</Badge>}
              </TableCell>
              <TableCell>
                {provName.get(m.provider_id) ?? m.provider_id}
              </TableCell>
              <TableCell>{m.upstream_model}</TableCell>
              <TableCell className='tabular-nums'>
                {yuan(m.price_input_micros)} / {yuan(m.price_cached_micros)} /{' '}
                {yuan(m.price_output_micros)}
              </TableCell>
              <TableCell>
                {m.enabled ? zh.common.enabled : zh.common.disabled}
              </TableCell>
              <TableCell className='space-x-1'>
                <TestButton run={() => modelApi.testModel(m.id)} />
                {!m.is_default && (
                  <Button
                    size='sm'
                    variant='ghost'
                    disabled={setDefault.isPending}
                    onClick={() => setDefault.mutate(m.id)}
                  >
                    {zh.models.setDefault}
                  </Button>
                )}
                <Button size='sm' variant='ghost' onClick={() => setAcl(m)}>
                  {zh.models.acl}
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  onClick={() => setEdit({ id: m.id, v: toForm(m) })}
                >
                  {zh.common.edit}
                </Button>
                <Button size='sm' variant='ghost' onClick={() => setDel(m)}>
                  {zh.common.delete}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      <Dialog open={!!edit} onOpenChange={(o) => !o && setEdit(null)}>
        <DialogContent className='max-h-[90vh] overflow-y-auto'>
          <DialogHeader>
            <DialogTitle>
              {edit?.id ? zh.models.editModel : zh.models.newModel}
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
              <Field label={zh.models.provider}>
                <NativeSelect
                  value={String(edit.v.provider_id ?? '')}
                  onChange={(v) => set({ provider_id: Number(v) })}
                  options={(providers.data?.items ?? []).map((p) => ({
                    value: String(p.id),
                    label: p.name,
                  }))}
                />
              </Field>
              <Field label={zh.models.upstreamModel}>
                <Input
                  placeholder='deepseek-chat'
                  value={edit.v.upstream_model ?? ''}
                  onChange={(e) => set({ upstream_model: e.target.value })}
                />
              </Field>
              <div className='grid grid-cols-2 gap-3'>
                <Field label={zh.models.thinking}>
                  <NativeSelect
                    value={edit.v.thinking ?? 'off'}
                    onChange={(v) => set({ thinking: v })}
                    options={[
                      { value: 'off', label: zh.models.thinkingOff },
                      { value: 'on', label: zh.models.thinkingOn },
                      {
                        value: 'provider_default',
                        label: zh.models.thinkingDefault,
                      },
                    ]}
                  />
                </Field>
                <Field label={zh.models.toolChoice}>
                  <NativeSelect
                    value={edit.v.tool_choice_policy ?? 'downgrade'}
                    onChange={(v) => set({ tool_choice_policy: v })}
                    options={[
                      { value: 'downgrade', label: zh.models.toolDowngrade },
                      { value: 'reject', label: zh.models.toolReject },
                    ]}
                  />
                </Field>
                <Field label={zh.models.contextWindow}>
                  <Input
                    type='number'
                    min={0}
                    value={edit.v.context_window ?? 0}
                    onChange={(e) =>
                      set({ context_window: Number(e.target.value) })
                    }
                  />
                </Field>
                <Field label={zh.models.maxOutput}>
                  <Input
                    type='number'
                    min={0}
                    value={edit.v.max_output_tokens ?? 0}
                    onChange={(e) =>
                      set({ max_output_tokens: Number(e.target.value) })
                    }
                  />
                </Field>
              </div>
              <p className='text-xs text-muted-foreground'>
                {zh.models.priceUnit}
              </p>
              <div className='grid grid-cols-3 gap-3'>
                <Field label={zh.models.priceIn}>
                  <Input
                    inputMode='decimal'
                    value={edit.v.pin}
                    onChange={(e) => set({ pin: e.target.value })}
                  />
                </Field>
                <Field label={zh.models.priceCached}>
                  <Input
                    inputMode='decimal'
                    value={edit.v.pcached}
                    onChange={(e) => set({ pcached: e.target.value })}
                  />
                </Field>
                <Field label={zh.models.priceOut}>
                  <Input
                    inputMode='decimal'
                    value={edit.v.pout}
                    onChange={(e) => set({ pout: e.target.value })}
                  />
                </Field>
              </div>
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
      {acl && <AclDialog model={acl} onClose={() => setAcl(null)} />}
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
