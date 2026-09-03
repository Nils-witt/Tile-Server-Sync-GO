import type { ReactNode } from 'react'
import { useAuth } from './AuthContext'
import type { Permissions } from '../api/types'

// RequirePermission/RequireSuperuser mirror the old server-side
// requirePermission/requireSuperuser page=true behavior (see
// internal/webserver/auth.go before the SPA rewrite): a logged-in user
// missing the permission sees a plain "forbidden" message in place of the
// page, rather than being redirected away. Actual enforcement still happens
// server-side on every API call regardless — this is purely so the UI
// doesn't render a page whose every action would just 403.
export function RequirePermission({ perm, children }: { perm: keyof Permissions; children: ReactNode }) {
  const { me } = useAuth()

  if (!me?.permissions[perm]) {
    return <main>{forbidden}</main>
  }

  return <>{children}</>
}

export function RequireSuperuser({ children }: { children: ReactNode }) {
  const { me } = useAuth()

  if (!me?.isSuperuser) {
    return <main>{forbidden}</main>
  }

  return <>{children}</>
}

const forbidden = <p>forbidden: you don't have permission to view this page</p>
