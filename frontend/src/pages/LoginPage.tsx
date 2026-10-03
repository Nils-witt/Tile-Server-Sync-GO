import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { startLogin } from '../auth/oidc'
import { Banner, type BannerState } from '../components/Banner'
import { Footer } from '../components/Footer'
import { ThemeToggle } from '../components/ThemeToggle'

export function LoginPage() {
  const [banner, setBanner] = useState<BannerState | null>(null)

  const [params] = useSearchParams()
  const next = params.get('next') || '/'
  const { ssoLabel } = useAuth()

  useEffect(() => {
    if (params.get('ssoerror')) {
      setBanner({ ok: false, text: 'SSO sign-in failed.' })
    }
    // Only ever needs to run once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function handleSSOLogin() {
    try {
      await startLogin(next)
    } catch (err) {
      setBanner({ ok: false, text: `SSO sign-in failed: ${err instanceof Error ? err.message : err}` })
    }
  }

  return (
    <>
      <ThemeToggle standalone />
      <main className="center-page">
        <div className="card">
          <h2 style={{ marginBottom: '0.9rem' }}>Sign in</h2>
          <Banner state={banner} />
          {ssoLabel ? (
            <button type="button" className="primary" style={{ width: '100%' }} onClick={handleSSOLogin}>
              {ssoLabel}
            </button>
          ) : (
            <p className="hint">Single sign-on is unavailable, so signing in is not possible right now.</p>
          )}
        </div>
      </main>
      <Footer />
    </>
  )
}
