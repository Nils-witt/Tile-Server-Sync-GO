import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from '../api/client'
import type { Me } from '../api/types'

interface AuthContextValue {
  /** true once the initial /api/me + /api/setup-status round trip has settled. */
  ready: boolean
  me: Me | null
  needsSetup: boolean
  refreshMe: () => Promise<void>
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
      setMe(await api.me())
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setMe(null)
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
      await Promise.all([refreshSetupStatus(), refreshMe()])
      setReady(true)
    })()
    // Runs once on mount; refreshMe/refreshSetupStatus are stable (useCallback, no deps).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const logout = useCallback(async () => {
    await api.logout()
    setMe(null)
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
