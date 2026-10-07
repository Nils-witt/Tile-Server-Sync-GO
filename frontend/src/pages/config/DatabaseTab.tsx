import { useEffect, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { Banner, type BannerState } from '../../components/Banner'
import type { DatabaseSection } from '../../api/types'

const COLUMN_FIELDS: [string, string][] = [
  ['uuid', 'UUID (upsert key)'], ['mapUuid', 'Map UUID'], ['version', 'Version'],
  ['name', 'Name'], ['externalId', 'External ID'], ['latitude', 'Latitude'],
  ['longitude', 'Longitude'], ['street', 'Street'], ['housenumber', 'House number'],
  ['postcode', 'Postcode'], ['city', 'City'], ['cityDistrict', 'City district'],
  ['createdAt', 'Created at'], ['updatedAt', 'Updated at'],
  ['createdBy', 'Created by'], ['updatedBy', 'Updated by'], ['syncedAt', 'Synced at (bookkeeping)'],
]

const empty: DatabaseSection = {
  host: '', port: 0, user: '', password: '', name: '', params: '', tls: false, tlsSkipVerify: false, tlsCaCert: '',
  table: '', pruneMissing: false, syncOverlays: false, columns: {} }

export function DatabaseTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigDatabase
  const [form, setForm] = useState<DatabaseSection>(empty)
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)

  useEffect(() => {
    api
      .getDatabaseSection()
      // res.database.columns is a Go map[string]string with no `omitempty`;
      // an unconfigured/empty map still serializes as JSON null, not {} —
      // default it back to {} so the column inputs below (form.columns[key])
      // don't throw on a fresh install.
      .then((res) => setForm({ ...res.database, password: '', columns: res.database.columns || {} }))
      .catch((err) => setBanner({ ok: false, text: `Failed to load config: ${err instanceof ApiError ? err.message : err}` }))
  }, [])

  function setColumn(key: string, value: string) {
    setForm((prev) => ({ ...prev, columns: { ...prev.columns, [key]: value } }))
  }

  function trimmedForm(): DatabaseSection {
    return {
      ...form,
      host: form.host.trim(),
      user: form.user.trim(),
      name: form.name.trim(),
      params: form.params.trim(),
      table: form.table.trim(),
      tlsCaCert: form.tlsCaCert.trim(),
    }
  }

  async function handleTest() {
    setTesting(true)

    try {
      const res = await api.testDatabaseSection(trimmedForm())
      if (res.ok) {
        setBanner({ ok: true, text: 'Connection succeeded (not saved yet).' })
      } else {
        setBanner({ ok: false, text: `Connection failed: ${res.error}` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Connection test failed: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setTesting(false)
    }
  }

  async function handleSave() {
    setSaving(true)

    try {
      const res = await api.saveDatabaseSection(trimmedForm())
      if (res.applied) {
        setBanner({ ok: true, text: 'Saved and applied to the running process.' })
      } else {
        setBanner({ ok: false, text: `Saved, but failed to apply to the running process: ${res.applyError}` })
      }
      if (res.config) setForm({ ...res.config.database, password: '', columns: res.config.database.columns || {} })
    } catch (err) {
      setBanner({ ok: false, text: `Not saved: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="card">
      <h2>Database</h2>
      <p className="hint">Where synced geo objects are written.</p>
      <Banner state={banner} />

      <label htmlFor="db-host">Host</label>
      <input
        type="text"
        id="db-host"
        placeholder="127.0.0.1"
        disabled={disabled}
        value={form.host}
        onChange={(e) => setForm({ ...form, host: e.target.value })}
      />
      <label htmlFor="db-port">Port</label>
      <input
        type="number"
        id="db-port"
        min={1}
        max={65535}
        placeholder="3306"
        disabled={disabled}
        value={form.port || ''}
        onChange={(e) => setForm({ ...form, port: Number(e.target.value) || 0 })}
      />
      <label htmlFor="db-user">User</label>
      <input
        type="text"
        id="db-user"
        autoComplete="off"
        disabled={disabled}
        value={form.user}
        onChange={(e) => setForm({ ...form, user: e.target.value })}
      />
      <label htmlFor="db-password">Password</label>
      <input
        type="password"
        id="db-password"
        autoComplete="new-password"
        placeholder="unchanged — leave blank to keep the current password"
        disabled={disabled}
        value={form.password}
        onChange={(e) => setForm({ ...form, password: e.target.value })}
      />
      <label htmlFor="db-name">Database name</label>
      <input
        type="text"
        id="db-name"
        placeholder="tileserve"
        disabled={disabled}
        value={form.name}
        onChange={(e) => setForm({ ...form, name: e.target.value })}
      />
      <label htmlFor="db-params">Extra parameters (optional, parseTime=true is always set)</label>
      <input
        type="text"
        id="db-params"
        placeholder="timeout=10s"
        disabled={disabled}
        value={form.params}
        onChange={(e) => setForm({ ...form, params: e.target.value })}
      />
      <div className="checkbox-row">
        <input
          type="checkbox"
          id="db-tls"
          disabled={disabled}
          checked={form.tls}
          onChange={(e) => setForm({ ...form, tls: e.target.checked })}
        />
        <label htmlFor="db-tls">Use TLS</label>
      </div>
      <div className="checkbox-row">
        <input
          type="checkbox"
          id="db-tls-verify"
          disabled={disabled || !form.tls}
          checked={form.tls && !form.tlsSkipVerify}
          onChange={(e) => setForm({ ...form, tlsSkipVerify: !e.target.checked })}
        />
        <label htmlFor="db-tls-verify">
          Verify the server certificate (uncheck only for a self-signed certificate on a trusted network)
        </label>
      </div>
      <label htmlFor="db-tls-ca">CA certificate (optional, PEM — leave blank to use the system's trusted CAs)</label>
      <textarea
        id="db-tls-ca"
        rows={4}
        spellCheck={false}
        placeholder={'-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----'}
        disabled={disabled || !form.tls || form.tlsSkipVerify}
        value={form.tlsCaCert}
        onChange={(e) => setForm({ ...form, tlsCaCert: e.target.value })}
      />
      <label htmlFor="db-table">Table</label>
      <input
        type="text"
        id="db-table"
        placeholder="geo_objects"
        disabled={disabled}
        value={form.table}
        onChange={(e) => setForm({ ...form, table: e.target.value })}
      />
      <div className="checkbox-row">
        <input
          type="checkbox"
          id="db-prune"
          disabled={disabled}
          checked={form.pruneMissing}
          onChange={(e) => setForm({ ...form, pruneMissing: e.target.checked })}
        />
        <label htmlFor="db-prune">Prune missing rows after each map/version sync</label>
      </div>
      <div className="checkbox-row">
        <input
          type="checkbox"
          id="db-sync-overlays"
          disabled={disabled}
          checked={form.syncOverlays}
          onChange={(e) => setForm({ ...form, syncOverlays: e.target.checked })}
        />
        <label htmlFor="db-sync-overlays">Keep map_src_overlays (EDP) rows in sync with map create/update/delete</label>
      </div>

      <details style={{ marginTop: '0.9rem' }}>
        <summary style={{ cursor: 'pointer', fontWeight: 500, fontSize: '0.85rem' }}>
          Column mapping (advanced — leave blank to use defaults)
        </summary>
        <div className="col-grid">
          {COLUMN_FIELDS.map(([key, label]) => (
            <div key={key}>
              <label htmlFor={`col-${key}`}>{label}</label>
              <input
                type="text"
                id={`col-${key}`}
                disabled={disabled}
                value={form.columns[key] || ''}
                onChange={(e) => setColumn(key, e.target.value)}
              />
            </div>
          ))}
        </div>
      </details>

      <div className="actions-row">
        <button type="button" className="primary" disabled={disabled || saving} onClick={handleSave}>
          Save database section
        </button>
        <button type="button" disabled={disabled || testing || saving} onClick={handleTest}>
          {testing ? 'Testing…' : 'Test connection'}
        </button>
      </div>
    </section>
  )
}
