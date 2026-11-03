import { useAuthStore } from '@/stores/auth-store'
import { useLayout } from '@/context/layout-provider'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarRail,
} from '@/components/ui/sidebar'
import { AppTitle } from './app-title'
import { sidebarData } from './data/sidebar-data'
import { NavGroup } from './nav-group'
import { NavUser } from './nav-user'

export function AppSidebar() {
  const { collapsible, variant } = useLayout()
  const user = useAuthStore((s) => s.auth.user)
  return (
    <Sidebar collapsible={collapsible} variant={variant}>
      <SidebarHeader>
        <AppTitle />
      </SidebarHeader>
      <SidebarContent>
        {sidebarData.navGroups
          .map((g) => ({
            ...g,
            items: g.items.filter(
              (i) => !i.perm || user?.permissions?.includes(i.perm)
            ),
          }))
          .filter((g) => g.items.length > 0)
          .map((props) => (
            <NavGroup key={props.title} {...props} />
          ))}
      </SidebarContent>
      <SidebarFooter>
        <NavUser
          user={{
            name: user?.display_name || user?.email || '',
            email: user?.email ?? '',
            avatar: '',
          }}
        />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
