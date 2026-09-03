import { useEffect, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { Banner, type BannerState } from '../../components/Banner'
import type { Permissions, SSOConfig } from '../../api/types'

const PERM_FIELDS: [keyof Permissions, string][] = [
  ['viewStatus', 'View status'],
  ['triggerSync', 'Trigger sync'],
  ['viewConfig', 'View config'],
  ['editConfigAPI', 'Edit config: API'],
  ['editConfigDatabase', 'Edit config: Database'],
  ['editConfigMaps', 'Edit config: Maps'],
  ['editConfigSSO', 'Edit config: SSO'],
]

const empty: SSOConfig = {
  enabled: false,
  issuerUrl: '',
  clientId: '',
  clientSecret: '',
  scopes: '',
  buttonLabel: '',
  redirectBaseUrl: '',
  defaultPermissions: {
    viewStatus: false,
    triggerSync: false,
    viewConfig: false,
    editConfigAPI: false,
    editConfigDatabase: false,
    editConfigMaps: false,
    editConfigSSO: false,
  },
}

export function SsoTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigSSO
  const [form, setForm] = useState<SSOConfig>(empty)
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getSSOConfig()
      .then((cfg) => setForm({ ...cfg, clientSecret: '' }))
      .catch((err) => setBanner({ ok: false, text: `Failed to load SSO config: ${err instanceof ApiError ? err.message : err}` }))
  }, [])

  function setPerm(key: keyof Permissions, value: boolean) {
    setForm((prev) => ({ ...prev, defaultPermissions: { ...prev.defaultPermissions, [key]: value } }))
  }

  async function handleSave() {
    setSaving(true)

    try {
      const saved = await api.saveSSOConfig({
        ...form,
        issuerUrl: form.issuerUrl.trim(),
        clientId: form.clientId.trim(),
        scopes: form.scopes.trim(),
        buttonLabel: form.buttonLabel.trim(),
        redirectBaseUrl: form.redirectBaseUrl.trim(),
      })
      setBanner({ ok: true, text: 'SSO settings saved.' })
      setForm({ ...saved, clientSecret: '' })
    } catch (err) {
      setBanner({ ok: false, text: `Not saved: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="card">
      <h2>SSO</h2>
      <p className="hint">
        Optional OpenID Connect single sign-on, in addition to local username/password accounts (which
        never go away). A first-time SSO login auto-creates a local account with the default
        permissions below unless a local account with the same username already exists, in which case
        it's linked instead (keeping that account's own permissions). SSO accounts are never made
        superusers automatically &mdash; grant that manually on the <a href="/users">Users page</a> if
        needed.
      </p>
      <Banner state={banner} />

      <div className="checkbox-row">
        <input
          type="checkbox"
          id="sso-enabled"
          disabled={disabled}
          checked={form.enabled}
          onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
        />
        <label htmlFor="sso-enabled">Enabled</label>
      </div>
      <label htmlFor="sso-issuer">Issuer URL</label>
      <input
        type="text"
        id="sso-issuer"
        placeholder="https://accounts.example.com"
        disabled={disabled}
        value={form.issuerUrl}
        onChange={(e) => setForm({ ...form, issuerUrl: e.target.value })}
      />
      <label htmlFor="sso-client-id">Client ID</label>
      <input
        type="text"
        id="sso-client-id"
        disabled={disabled}
        value={form.clientId}
        onChange={(e) => setForm({ ...form, clientId: e.target.value })}
      />
      <label htmlFor="sso-client-secret">Client secret</label>
      <input
        type="text"
        id="sso-client-secret"
        placeholder="unchanged — leave blank to keep the current secret"
        disabled={disabled}
        value={form.clientSecret}
        onChange={(e) => setForm({ ...form, clientSecret: e.target.value })}
      />
      <label htmlFor="sso-scopes">Scopes (space-separated)</label>
      <input
        type="text"
        id="sso-scopes"
        placeholder="openid profile email"
        disabled={disabled}
        value={form.scopes}
        onChange={(e) => setForm({ ...form, scopes: e.target.value })}
      />
      <label htmlFor="sso-button-label">Login page button label</label>
      <input
        type="text"
        id="sso-button-label"
        placeholder="Sign in with SSO"
        disabled={disabled}
        value={form.buttonLabel}
        onChange={(e) => setForm({ ...form, buttonLabel: e.target.value })}
      />
      <label htmlFor="sso-redirect-base">
        Redirect base URL (advanced — leave blank to auto-detect from the incoming request; set this
        when running behind a reverse proxy)
      </label>
      <input
        type="text"
        id="sso-redirect-base"
        placeholder="https://sync.example.com"
        disabled={disabled}
        value={form.redirectBaseUrl}
        onChange={(e) => setForm({ ...form, redirectBaseUrl: e.target.value })}
      />

      <p className="hint" style={{ marginTop: '0.9rem' }}>
        Default permissions for a newly auto-created SSO account:
      </p>
      {PERM_FIELDS.map(([key, label]) => (
        <div className="checkbox-row" key={key}>
          <input
            type="checkbox"
            id={`sso-default-${key}`}
            disabled={disabled}
            checked={form.defaultPermissions[key]}
            onChange={(e) => setPerm(key, e.target.checked)}
          />
          <label htmlFor={`sso-default-${key}`}>{label}</label>
        </div>
      ))}

      <div className="actions-row">
        <button type="button" className="primary" disabled={disabled || saving} onClick={handleSave}>
          Save SSO section
        </button>
      </div>
    </section>
  )
}
