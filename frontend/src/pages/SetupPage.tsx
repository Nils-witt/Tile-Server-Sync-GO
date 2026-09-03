import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { Banner, type BannerState } from '../components/Banner'
import { Footer } from '../components/Footer'
import { ThemeToggle } from '../components/ThemeToggle'

export function SetupPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const navigate = useNavigate()
  const { refreshMe, refreshSetupStatus } = useAuth()

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSubmitting(true)

    try {
      await api.setup(username, password)
      await Promise.all([refreshMe(), refreshSetupStatus()])
      navigate('/', { replace: true })
    } catch (err) {
      setBanner({ ok: false, text: err instanceof ApiError ? err.message : `Setup failed: ${err}` })
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <>
      <ThemeToggle standalone />
      <main className="center-page wide">
        <div className="card">
          <h2 style={{ marginBottom: '0.3rem' }}>Create the first account</h2>
          <p className="hint" style={{ marginBottom: '0.9rem' }}>
            No accounts exist yet. The account you create here is a full administrator, with every
            permission and access to user management &mdash; you can create more restricted accounts
            afterwards from the Users page.
          </p>
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
              autoComplete="new-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <div className="actions-row">
              <button type="submit" className="primary" disabled={submitting}>
                Create account
              </button>
            </div>
          </form>
        </div>
      </main>
      <Footer />
    </>
  )
}
