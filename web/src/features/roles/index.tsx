import { useQuery } from '@tanstack/react-query'
import { roleApi } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

// 只读角色矩阵：内置角色 × 权限点。
export function RolesPage() {
  const m = useQuery({ queryKey: ['roles'], queryFn: roleApi.matrix })
  const roles = m.data?.roles ?? []
  return (
    <>
      <Header>
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <h1 className='mb-4 text-2xl font-bold tracking-tight'>角色与权限</h1>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>权限点</TableHead>
              {roles.map((r) => (
                <TableHead key={r.key} className='text-center'>
                  {r.name}
                  {r.privileged && (
                    <Badge variant='outline' className='ms-1'>
                      特权
                    </Badge>
                  )}
                  {r.dept_scoped && (
                    <div className='text-xs text-muted-foreground'>按部门</div>
                  )}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {(m.data?.permissions ?? []).map((p) => (
              <TableRow key={p}>
                <TableCell className='font-mono text-xs'>{p}</TableCell>
                {roles.map((r) => (
                  <TableCell key={r.key} className='text-center'>
                    {r.permissions.includes(p) ? '✓' : ''}
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Main>
    </>
  )
}
