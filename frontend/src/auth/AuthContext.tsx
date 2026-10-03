import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from '../api/client'
import type { Me } from '../api/types'
import { initOidc, logoutOidc, onSsoSessionEnded } from './oidc'

interface AuthContextValue {
  /** true once the initial /api/sso/status + /api/me round trip has settled. */
  ready: boolean
  me: Me | null
  /** The SSO login button's label, or null when SSO is unavailable (nobody can sign in). */
  ssoLabel: string | null
  /**
   * Reloads the current account; resolves to it, or null when not signed in.
   * Pass login=true only right after the provider callback, so the server
   * records the sign-in once (a plain reload is not logged).
   */
  refreshMe: (login?: boolean) => Promise<Me | null>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [ready, setReady] = useState(false)
  const [me, setMe] = useState<Me | null>(null)
  const [ssoLabel, setSsoLabel] = useState<string | null>(null)

  const refreshMe = useCallback(async (login = false) => {
    try {
      const current = await (login ? api.ssoLogin() : api.me())
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

  useEffect(() => {
    void (async () => {
      // SSO settings first: a token stored by an earlier page load must be
      // attached to the very first /api/me (see oidc.ts / client.ts).
      try {
        const status = await api.ssoStatus()
        initOidc(status)
        if (status.enabled) setSsoLabel(status.buttonLabel || 'Sign in with SSO')
        // An SSO session that can't be renewed any more counts as logged
        // out: AuthGate then sends the user to /login?next=<current page>.
        onSsoSessionEnded(() => setMe(null))
      } catch {
        /* SSO status unavailable: the login page says so. */
      }

      await refreshMe()
      setReady(true)
    })()
    // Runs once on mount; refreshMe is stable (useCallback, no deps).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const logout = useCallback(async () => {
    setMe(null)
    // This may also navigate away, to the provider's logout.
    await logoutOidc()
  }, [])

  const value = useMemo(
    () => ({ ready, me, ssoLabel, refreshMe, logout }),
    [ready, me, ssoLabel, refreshMe, logout],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
