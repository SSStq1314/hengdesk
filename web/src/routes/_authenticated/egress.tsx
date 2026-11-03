import { createFileRoute } from '@tanstack/react-router'
import { EgressPage } from '@/features/egress'

export const Route = createFileRoute('/_authenticated/egress')({
  component: EgressPage,
})
