import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { Banner, type BannerState } from '../components/Banner'
import { Footer } from '../components/Footer'
import { ThemeToggle } from '../components/ThemeToggle'

export function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [ssoLabel, setSsoLabel] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const [params] = useSearchParams()
  const next = params.get('next') || '/'
  const navigate = useNavigate()
  const { refreshMe } = useAuth()

  useEffect(() => {
    if (params.get('ssoerror')) {
      setBanner({ ok: false, text: 'SSO sign-in failed.' })
    }

    api
      .ssoStatus()
      .then((status) => {
        if (status.enabled) setSsoLabel(status.buttonLabel || 'Sign in with SSO')
      })
      .catch(() => {
        /* SSO status unavailable: leave the button hidden. */
      })
    // Only ever needs to run once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSubmitting(true)

    try {
      await api.login(username, password)
      await refreshMe()
      navigate(next, { replace: true })
    } catch (err) {
      setBanner({ ok: false, text: err instanceof ApiError ? err.message : `Sign in failed: ${err}` })
    } finally {
      setSubmitting(false)
    }
  }

  function handleSSOLogin() {
    window.location.href = `/login/sso?next=${encodeURIComponent(next)}`
  }

  return (
    <>
      <ThemeToggle standalone />
      <main className="center-page">
        <div className="card">
          <h2 style={{ marginBottom: '0.9rem' }}>Sign in</h2>
          <Banner state={banner} />
          <form onSubmit={handleSubmit}>
            <label htmlFor="username">Username</label>
            <input
              type="text"
              id="username"
              autoComplete="username"
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
            <label htmlFor="password">Password</label>
            <input
              type="password"
              id="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <div className="actions-row">
              <button type="submit" className="primary" disabled={submitting}>
                Sign in
              </button>
            </div>
          </form>
          {ssoLabel && (
            <>
              <div className="hint" style={{ margin: '0.9rem 0', textAlign: 'center' }}>
                or
              </div>
              <button type="button" className="primary" style={{ width: '100%' }} onClick={handleSSOLogin}>
                {ssoLabel}
              </button>
            </>
          )}
        </div>
      </main>
      <Footer />
    </>
  )
}
