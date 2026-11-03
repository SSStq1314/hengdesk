import type { Department } from '@/lib/api'

export type DeptNode = Department & { depth: number; path: string }

// 扁平列表 → 先序遍历（带层级与完整路径），用于缩进显示和下拉选择。
export function flattenTree(items: Department[]): DeptNode[] {
  const byParent = new Map<number | null, Department[]>()
  for (const d of items) {
    const k = d.parent_id ?? null
    byParent.set(k, [...(byParent.get(k) ?? []), d])
  }
  const out: DeptNode[] = []
  const walk = (parent: number | null, depth: number, prefix: string) => {
    for (const d of (byParent.get(parent) ?? []).sort((a, b) =>
      a.name.localeCompare(b.name, 'zh-CN')
    )) {
      const path = prefix ? `${prefix}/${d.name}` : d.name
      out.push({ ...d, depth, path })
      walk(d.id, depth + 1, path)
    }
  }
  walk(null, 0, '')
  return out
}
