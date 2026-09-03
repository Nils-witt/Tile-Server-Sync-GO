import { useEffect } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from './auth/AuthContext'
import { RequirePermission, RequireSuperuser } from './auth/guards'
import { ProtectedLayout } from './components/ProtectedLayout'
import { LoginPage } from './pages/LoginPage'
import { SetupPage } from './pages/SetupPage'
import { StatusPage } from './pages/StatusPage'
import { UsersPage } from './pages/UsersPage'
import { SecurityLogPage } from './pages/SecurityLogPage'
import { ConfigPage } from './pages/config/ConfigPage'

// AuthGate holds the redirect rules the old server enforced with real HTTP
// 302s (setupGate/requireUser's page=true branch in the pre-SPA
// internal/webserver/auth.go): while setup hasn't happened yet, every route
// but /setup bounces there; once it has, /setup bounces to /login;
// unauthenticated access to anything but /login bounces to /login?next=...;
// and being authenticated on /login or /setup bounces to /.
function AuthGate() {
  const { ready, me, needsSetup } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (!ready) return

    const path = location.pathname

    if (needsSetup) {
      if (path !== '/setup') navigate('/setup', { replace: true })
      return
    }

    if (path === '/setup') {
      navigate('/login', { replace: true })
      return
    }

    if (!me) {
      if (path !== '/login') {
        navigate(`/login?next=${encodeURIComponent(path + location.search)}`, { replace: true })
      }
      return
    }

    if (path === '/login') {
      navigate('/', { replace: true })
    }
  }, [ready, me, needsSetup, location.pathname, location.search, navigate])

  if (!ready) return null

  return (
    <Routes>
      <Route path="/setup" element={<SetupPage />} />
      <Route path="/login" element={<LoginPage />} />
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
          path="/users"
          element={
            <RequireSuperuser>
              <UsersPage />
            </RequireSuperuser>
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
