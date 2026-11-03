import type { ReactNode } from 'react'
import { Label } from '@/components/ui/label'

export function Field(props: { label: string; children: ReactNode }) {
  return (
    <div className='grid gap-1.5'>
      <Label>{props.label}</Label>
      {props.children}
    </div>
  )
}

export function NativeSelect(props: {
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
  ariaLabel?: string
}) {
  return (
    <select
      aria-label={props.ariaLabel}
      className='h-9 rounded-md border border-input bg-background px-3 text-sm'
      value={props.value}
      onChange={(e) => props.onChange(e.target.value)}
    >
      {props.options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  )
}
