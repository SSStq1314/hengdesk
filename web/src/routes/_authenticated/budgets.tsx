import { createFileRoute } from '@tanstack/react-router'
import { BudgetsPage } from '@/features/budgets'

export const Route = createFileRoute('/_authenticated/budgets')({
  component: BudgetsPage,
})
