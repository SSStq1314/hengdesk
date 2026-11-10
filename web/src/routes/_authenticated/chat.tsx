import { z } from 'zod'
import { createFileRoute } from '@tanstack/react-router'
import { ChatPage } from '@/features/chat'

const searchSchema = z.object({
  agent: z.coerce.number().int().positive().optional(),
  debug: z.coerce.boolean().optional(),
  conv: z.coerce.number().int().positive().optional(),
})

export const Route = createFileRoute('/_authenticated/chat')({
  component: ChatPage,
  validateSearch: searchSchema,
})
