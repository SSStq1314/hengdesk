import { type LinkProps } from '@tanstack/react-router'

type BaseNavItem = {
  title: string
  badge?: string
  icon?: React.ElementType
  /** 需要的权限点；无该权限时不显示（后端仍会 404） */
  perm?: string
}

type NavLink = BaseNavItem & {
  url: LinkProps['to'] | (string & {})
  items?: never
}

type NavCollapsible = BaseNavItem & {
  items: (BaseNavItem & { url: LinkProps['to'] | (string & {}) })[]
  url?: never
}

type NavItem = NavCollapsible | NavLink

type NavGroup = {
  title: string
  items: NavItem[]
}

type SidebarData = {
  navGroups: NavGroup[]
}

export type { SidebarData, NavGroup, NavItem, NavCollapsible, NavLink }
