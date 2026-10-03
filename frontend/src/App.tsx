import { useEffect } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from './auth/AuthContext'
import { RequirePermission, RequireSuperuser } from './auth/guards'
import { ProtectedLayout } from './components/ProtectedLayout'
import { LoginPage } from './pages/LoginPage'
import { SsoCallbackPage } from './pages/SsoCallbackPage'
import { SSO_CALLBACK_PATH } from './auth/oidc'
import { StatusPage } from './pages/StatusPage'
import { SecurityLogPage } from './pages/SecurityLogPage'
import { ConfigPage } from './pages/config/ConfigPage'

// AuthGate holds the client-side redirect rules: unauthenticated access to
// anything but /login bounces to /login?next=..., and being authenticated on
// /login bounces to /. The SSO callback route is exempt from all of this
// (see SsoCallbackPage).
function AuthGate() {
  const { ready, me } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (!ready) return

    const path = location.pathname

    // The SSO callback page finishes the provider's login and navigates on its
    // own; redirecting away first would lose the authorization response.
    if (path === SSO_CALLBACK_PATH) return

    if (!me) {
      if (path !== '/login') {
        navigate(`/login?next=${encodeURIComponent(path + location.search)}`, { replace: true })
      }
      return
    }

    if (path === '/login') {
      navigate('/', { replace: true })
    }
  }, [ready, me, location.pathname, location.search, navigate])

  if (!ready) return null

  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path={SSO_CALLBACK_PATH} element={<SsoCallbackPage />} />
      <Route element={me ? <ProtectedLayout /> : <NullElement />}>
        <Route
          path="/"
          element={
            <RequirePermission perm="viewStatus">
              <StatusPage />
            </RequirePermission>
          }
        />
        <Route
          path="/config/*"
          element={
            <RequirePermission perm="viewConfig">
              <ConfigPage />
            </RequirePermission>
          }
        />
        <Route
          path="/security-log"
          element={
            <RequireSuperuser>
              <SecurityLogPage />
            </RequireSuperuser>
          }
        />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

// NullElement covers the render pass before AuthGate's effect has navigated
// an unauthenticated visitor away from a protected route.
function NullElement() {
  return null
}

export default function App() {
  return <AuthGate />
}
