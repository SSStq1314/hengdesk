import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { zh } from '@/locales/zh-CN'
import { toast } from 'sonner'
import { agentApi } from '@/lib/api'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/features/models/fields'

const t = zh.agents

export function AgentCreateDialog(props: { onClose: () => void }) {
  const qc = useQueryClient()
  const nav = useNavigate()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const create = useMutation({
    mutationFn: () =>
      agentApi.create({
        name: name.trim(),
        description: description.trim(),
        max_steps: 5,
      }),
    onSuccess: (a) => {
      qc.invalidateQueries({ queryKey: ['agents'] })
      props.onClose()
      nav({ to: '/admin/agents/$id', params: { id: String(a.id) } })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Dialog open onOpenChange={(o) => !o && props.onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t.create}</DialogTitle>
        </DialogHeader>
        <div className='grid gap-3'>
          <Field label={t.name}>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
            />
          </Field>
          <Field label={t.description}>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
            />
          </Field>
        </div>
        <DialogFooter>
          <Button variant='outline' onClick={props.onClose}>
            {zh.common.cancel}
          </Button>
          <Button
            disabled={!name.trim() || create.isPending}
            onClick={() => {
              if (!name.trim()) {
                toast.error(t.emptyName)
                return
              }
              create.mutate()
            }}
          >
            {t.create}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
