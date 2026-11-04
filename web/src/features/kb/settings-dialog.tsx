import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { kbApi, type KB } from '@/lib/api'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/features/models/fields'

const t = zh.kb

// KBDialog 新建（kb 为空）或修改知识库设置。修改 embedding 模型 / 切片参数会触发全部文件重建索引。
export function KBDialog(props: {
  kb?: KB
  onClose: () => void
  onSaved?: (k: KB) => void
}) {
  const qc = useQueryClient()
  const k = props.kb
  const [name, setName] = useState(k?.name ?? '')
  const [desc, setDesc] = useState(k?.description ?? '')
  const [model, setModel] = useState(k?.embedding_model ?? '')
  const [size, setSize] = useState(String(k?.chunk_size ?? 800))
  const [overlap, setOverlap] = useState(String(k?.chunk_overlap ?? 100))
  const [strict, setStrict] = useState(k?.strict_grounding ?? false)
  const save = useMutation({
    mutationFn: () => {
      const b = {
        name: name.trim(),
        description: desc,
        embedding_model: model.trim() || undefined,
        chunk_size: Number(size),
        chunk_overlap: Number(overlap),
        strict_grounding: strict,
      }
      return k ? kbApi.update(k.id, b) : kbApi.create(b)
    },
    onSuccess: (res) => {
      toast.success(t.saved)
      qc.invalidateQueries({ queryKey: ['kbs'] })
      qc.invalidateQueries({ queryKey: ['kb', res.id] })
      props.onSaved?.(res)
      props.onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent className='max-w-lg'>
        <DialogHeader>
          <DialogTitle>{k ? t.settings : t.create}</DialogTitle>
        </DialogHeader>
        <div className='grid gap-3'>
          <Field label={t.name}>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label={t.description}>
            <Textarea
              rows={2}
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
            />
          </Field>
          <Field label={t.embeddingModel}>
            <Input value={model} onChange={(e) => setModel(e.target.value)} />
          </Field>
          <p className='text-xs text-muted-foreground'>{t.embeddingHint}</p>
          <div className='grid grid-cols-2 gap-3'>
            <Field label={t.chunkSize}>
              <Input
                type='number'
                value={size}
                onChange={(e) => setSize(e.target.value)}
              />
            </Field>
            <Field label={t.chunkOverlap}>
              <Input
                type='number'
                value={overlap}
                onChange={(e) => setOverlap(e.target.value)}
              />
            </Field>
          </div>
          <p className='text-xs text-muted-foreground'>{t.chunkHint}</p>
          <div className='flex items-center justify-between gap-3'>
            <Label htmlFor='kb-strict'>{t.strictGrounding}</Label>
            <Switch
              id='kb-strict'
              checked={strict}
              onCheckedChange={setStrict}
            />
          </div>
          <p className='text-xs text-muted-foreground'>
            {t.strictGroundingHint}
          </p>
        </div>
        <DialogFooter>
          <Button variant='outline' onClick={props.onClose}>
            {t.cancel}
          </Button>
          <Button
            disabled={!name.trim() || save.isPending}
            onClick={() => save.mutate()}
          >
            {t.save}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
