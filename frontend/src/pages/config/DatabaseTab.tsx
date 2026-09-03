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

const empty: DatabaseSection = { dsn: '', table: '', pruneMissing: false, syncOverlays: false, columns: {} }

export function DatabaseTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigDatabase
  const [form, setForm] = useState<DatabaseSection>(empty)
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getDatabaseSection()
      // res.database.columns is a Go map[string]string with no `omitempty`;
      // an unconfigured/empty map still serializes as JSON null, not {} —
      // default it back to {} so the column inputs below (form.columns[key])
      // don't throw on a fresh install.
      .then((res) => setForm({ ...res.database, dsn: '', columns: res.database.columns || {} }))
      .catch((err) => setBanner({ ok: false, text: `Failed to load config: ${err instanceof ApiError ? err.message : err}` }))
  }, [])

  function setColumn(key: string, value: string) {
    setForm((prev) => ({ ...prev, columns: { ...prev.columns, [key]: value } }))
  }

  async function handleSave() {
    setSaving(true)

    try {
      const res = await api.saveDatabaseSection({ ...form, dsn: form.dsn.trim(), table: form.table.trim() })
      if (res.applied) {
        setBanner({ ok: true, text: 'Saved and applied to the running process.' })
      } else {
        setBanner({ ok: false, text: `Saved, but failed to apply to the running process: ${res.applyError}` })
      }
      if (res.config) setForm({ ...res.config.database, dsn: '', columns: res.config.database.columns || {} })
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

      <label htmlFor="db-dsn">DSN</label>
      <input
        type="text"
        id="db-dsn"
        placeholder="unchanged — leave blank to keep the current DSN"
        disabled={disabled}
        value={form.dsn}
        onChange={(e) => setForm({ ...form, dsn: e.target.value })}
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
      </div>
    </section>
  )
}
