import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from '../api/client'
import type { Me } from '../api/types'
import { initOidc, logoutOidc, onSsoSessionEnded } from './oidc'

interface AuthContextValue {
  /** true once the initial /api/me + /api/setup-status round trip has settled. */
  ready: boolean
  me: Me | null
  needsSetup: boolean
  /** Reloads the current account; resolves to it, or null when not signed in. */
  refreshMe: () => Promise<Me | null>
  refreshSetupStatus: () => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [ready, setReady] = useState(false)
  const [me, setMe] = useState<Me | null>(null)
  const [needsSetup, setNeedsSetup] = useState(false)

  const refreshMe = useCallback(async () => {
    try {
      const current = await api.me()
      setMe(current)
      return current
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setMe(null)
        return null
      } else {
        throw err
      }
    }
  }, [])

  const refreshSetupStatus = useCallback(async () => {
    const status = await api.setupStatus()
    setNeedsSetup(status.needsSetup)
  }, [])

  useEffect(() => {
    void (async () => {
      // SSO settings first: a token stored by an earlier page load must be
      // attached to the very first /api/me (see oidc.ts / client.ts).
      try {
        initOidc(await api.ssoStatus())
        // An SSO session that can't be renewed any more counts as logged
        // out: AuthGate then sends the user to /login?next=<current page>.
        onSsoSessionEnded(() => setMe(null))
      } catch {
        /* SSO status unavailable: carry on with local login only. */
      }

      await Promise.all([refreshSetupStatus(), refreshMe()])
      setReady(true)
    })()
    // Runs once on mount; refreshMe/refreshSetupStatus are stable (useCallback, no deps).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const logout = useCallback(async () => {
    await api.logout()
    setMe(null)
    // For an SSO user this may also navigate away, to the provider's logout.
    await logoutOidc()
  }, [])

  const value = useMemo(
    () => ({ ready, me, needsSetup, refreshMe, refreshSetupStatus, logout }),
    [ready, me, needsSetup, refreshMe, refreshSetupStatus, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
