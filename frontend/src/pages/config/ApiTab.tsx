import { useEffect, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { Banner, type BannerState } from '../../components/Banner'
import type { ApiSection } from '../../api/types'

const empty: ApiSection = { baseUrl: '', username: '', password: '', token: '' }

export function ApiTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigAPI
  const [form, setForm] = useState<ApiSection>(empty)
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getAPISection()
      .then((res) => setForm({ ...res.api, password: '' }))
      .catch((err) => setBanner({ ok: false, text: `Failed to load config: ${err instanceof ApiError ? err.message : err}` }))
  }, [])

  async function handleSave() {
    setSaving(true)

    try {
      const res = await api.saveAPISection({ ...form, baseUrl: form.baseUrl.trim(), token: form.token.trim() })
      if (res.applied) {
        setBanner({ ok: true, text: 'Saved and applied to the running process.' })
      } else {
        setBanner({ ok: false, text: `Saved, but failed to apply to the running process: ${res.applyError}` })
      }
      if (res.config) setForm({ ...res.config.api, password: '' })
    } catch (err) {
      setBanner({ ok: false, text: `Not saved: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="card">
      <h2>API</h2>
      <p className="hint">Credentials for the tileserve-go instance objects are fetched from.</p>
      <Banner state={banner} />

      <label htmlFor="api-baseurl">Base URL</label>
      <input
        type="text"
        id="api-baseurl"
        placeholder="http://localhost:8085"
        disabled={disabled}
        value={form.baseUrl}
        onChange={(e) => setForm({ ...form, baseUrl: e.target.value })}
      />
      <label htmlFor="api-username">Username</label>
      <input
        type="text"
        id="api-username"
        disabled={disabled}
        value={form.username}
        onChange={(e) => setForm({ ...form, username: e.target.value })}
      />
      <label htmlFor="api-password">Password</label>
      <input
        type="text"
        id="api-password"
        placeholder="unchanged — leave blank to keep the current password"
        disabled={disabled}
        value={form.password}
        onChange={(e) => setForm({ ...form, password: e.target.value })}
      />
      <label htmlFor="api-token">Token (used instead of username/password if set)</label>
      <input
        type="text"
        id="api-token"
        disabled={disabled}
        value={form.token}
        onChange={(e) => setForm({ ...form, token: e.target.value })}
      />
      <div className="actions-row">
        <button type="button" className="primary" disabled={disabled || saving} onClick={handleSave}>
          Save API section
        </button>
      </div>
    </section>
  )
}
