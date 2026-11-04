import { z } from 'zod'
import { createFileRoute } from '@tanstack/react-router'
import { ApprovalsPage } from '@/features/approvals'

const searchSchema = z.object({
  id: z.coerce.number().int().positive().optional(),
})

export const Route = createFileRoute('/_authenticated/approvals')({
  component: ApprovalsPage,
  validateSearch: searchSchema,
})
