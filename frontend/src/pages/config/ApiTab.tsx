import { useEffect, useId, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { Banner, type BannerState } from '../../components/Banner'
import type { ApiTarget } from '../../api/types'

const empty: ApiTarget = { id: '', name: '', baseUrl: '', username: '', password: '', token: '' }

function errorText(err: unknown): string {
  return err instanceof ApiError ? err.message : String(err)
}

function trimmed(a: ApiTarget): ApiTarget {
  return { ...a, id: a.id.trim(), name: a.name.trim(), baseUrl: a.baseUrl.trim(), token: a.token.trim() }
}

async function testConnection(a: ApiTarget, setBanner: (b: BannerState) => void) {
  try {
    const res = await api.testAPI(trimmed(a))
    if (res.ok) {
      setBanner({ ok: true, text: 'Connection succeeded (not saved yet).' })
    } else {
      setBanner({ ok: false, text: `Connection failed: ${res.error}` })
    }
  } catch (err) {
    setBanner({ ok: false, text: `Connection test failed: ${errorText(err)}` })
  }
}

export function ApiTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigApi
  const [apis, setApis] = useState<ApiTarget[]>([])
  const [banner, setBanner] = useState<BannerState | null>(null)

  useEffect(() => {
    api
      .listAPIs()
      .then((list) => setApis(list || []))
      .catch((err) => setBanner({ ok: false, text: `Failed to load APIs: ${errorText(err)}` }))
  }, [])

  return (
    <>
      <section className="card">
        <h2>APIs</h2>
        <p className="hint">The tileserve-go instances objects are fetched from. Each map picks one of them.</p>
        <Banner state={banner} />

        {apis.length === 0 && <p className="hint">No APIs configured yet — add one below.</p>}
        <div>
          {apis.map((a) => (
            <ApiCard
              key={a.id}
              initial={a}
              disabled={disabled}
              onRemoved={() => setApis((prev) => prev.filter((x) => x.id !== a.id))}
            />
          ))}
        </div>
      </section>

      {!disabled && <NewApiCard onAdded={(a) => setApis((prev) => [...prev, a])} />}
    </>
  )
}

interface FieldsProps {
  value: ApiTarget
  onChange: (a: ApiTarget) => void
  disabled: boolean
  /** Whether the id can still be edited (only before the API is created). */
  editableId: boolean
  /** Whether a password is already stored, so a blank one keeps it. */
  hasStoredPassword: boolean
}

function ApiFields({ value, onChange, disabled, editableId, hasStoredPassword }: FieldsProps) {
  const prefix = useId()

  return (
    <>
      <label htmlFor={`${prefix}-id`}>ID (letters, digits, "-" or "_"; can't be changed later)</label>
      <input
        type="text"
        id={`${prefix}-id`}
        placeholder="main"
        disabled={disabled || !editableId}
        value={value.id}
        onChange={(e) => onChange({ ...value, id: e.target.value })}
      />
      <label htmlFor={`${prefix}-name`}>Name (optional)</label>
      <input
        type="text"
        id={`${prefix}-name`}
        disabled={disabled}
        value={value.name}
        onChange={(e) => onChange({ ...value, name: e.target.value })}
      />
      <label htmlFor={`${prefix}-baseurl`}>Base URL</label>
      <input
        type="text"
        id={`${prefix}-baseurl`}
        placeholder="http://localhost:8085"
        disabled={disabled}
        value={value.baseUrl}
        onChange={(e) => onChange({ ...value, baseUrl: e.target.value })}
      />
      <label htmlFor={`${prefix}-username`}>Username</label>
      <input
        type="text"
        id={`${prefix}-username`}
        disabled={disabled}
        value={value.username}
        onChange={(e) => onChange({ ...value, username: e.target.value })}
      />
      <label htmlFor={`${prefix}-password`}>Password</label>
      <input
        type="text"
        id={`${prefix}-password`}
        placeholder={hasStoredPassword ? 'unchanged — leave blank to keep the current password' : ''}
        disabled={disabled}
        value={value.password}
        onChange={(e) => onChange({ ...value, password: e.target.value })}
      />
      <label htmlFor={`${prefix}-token`}>Token (used instead of username/password if set)</label>
      <input
        type="text"
        id={`${prefix}-token`}
        disabled={disabled}
        value={value.token}
        onChange={(e) => onChange({ ...value, token: e.target.value })}
      />
    </>
  )
}

interface CardProps {
  initial: ApiTarget
  disabled: boolean
  onRemoved: () => void
}

function ApiCard({ initial, disabled, onRemoved }: CardProps) {
  const [form, setForm] = useState<ApiTarget>({ ...initial, password: '' })
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [busy, setBusy] = useState<'saving' | 'testing' | 'removing' | null>(null)

  async function handleSave() {
    setBusy('saving')
    try {
      const res = await api.updateAPI(initial.id, trimmed(form))
      if (!res.api) {
        setBanner({ ok: false, text: `Not saved: ${res.error}` })
      } else if (!res.applied) {
        setBanner({ ok: false, text: `Connection tested and saved, but the full config could not be applied to the running process yet: ${res.applyError}` })
      } else if (res.overlayError) {
        setBanner({ ok: false, text: `Saved and applied, but failed to sync EDP overlays: ${res.overlayError}` })
      } else {
        setBanner({ ok: true, text: 'Connection tested, saved and applied to the running process.' })
      }
      if (res.api) setForm({ ...res.api, password: '' })
    } catch (err) {
      setBanner({ ok: false, text: `Not saved: ${errorText(err)}` })
    } finally {
      setBusy(null)
    }
  }

  async function handleTest() {
    setBusy('testing')
    await testConnection(form, setBanner)
    setBusy(null)
  }

  async function handleRemove() {
    setBusy('removing')
    try {
      const res = await api.deleteAPI(initial.id)
      if (res.ok) {
        onRemoved()
      } else {
        setBanner({ ok: false, text: `Failed to remove API: ${res.error}` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Failed to remove API: ${errorText(err)}` })
    } finally {
      setBusy(null)
    }
  }

  return (
    <details className="map-card">
      <summary>
        <span className="map-summary-id">{form.name.trim() || initial.id}</span>
        <span className="map-summary-meta">{form.baseUrl.trim()}</span>
      </summary>
      <div className="map-body">
        <ApiFields value={form} onChange={setForm} disabled={disabled} editableId={false} hasStoredPassword />
        <div className="actions-row">
          <button type="button" className="primary" disabled={disabled || busy !== null} onClick={handleSave}>
            {busy === 'saving' ? 'Testing & saving…' : 'Save'}
          </button>
          <button type="button" disabled={disabled || busy !== null} onClick={handleTest}>
            {busy === 'testing' ? 'Testing…' : 'Test connection'}
          </button>
          <button type="button" className="danger" disabled={disabled || busy !== null} onClick={handleRemove}>
            Remove API
          </button>
        </div>
        <Banner state={banner} />
      </div>
    </details>
  )
}

function NewApiCard({ onAdded }: { onAdded: (a: ApiTarget) => void }) {
  const [form, setForm] = useState<ApiTarget>(empty)
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [busy, setBusy] = useState<'saving' | 'testing' | null>(null)

  async function handleAdd() {
    setBusy('saving')
    try {
      const res = await api.createAPI(trimmed(form))
      if (!res.api) {
        setBanner({ ok: false, text: `Not added: ${res.error}` })
        return
      }

      onAdded(res.api)
      setForm(empty)
      if (res.applied) {
        setBanner({ ok: true, text: `Added API "${res.api.id}".` })
      } else {
        setBanner({ ok: false, text: `Added API "${res.api.id}", but the full config could not be applied to the running process yet: ${res.applyError}` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Not added: ${errorText(err)}` })
    } finally {
      setBusy(null)
    }
  }

  async function handleTest() {
    setBusy('testing')
    await testConnection(form, setBanner)
    setBusy(null)
  }

  return (
    <section className="card">
      <h2>Add API</h2>
      <p className="hint">The connection is tested before the API is added.</p>
      <Banner state={banner} />
      <ApiFields value={form} onChange={setForm} disabled={false} editableId hasStoredPassword={false} />
      <div className="actions-row">
        <button type="button" className="primary" disabled={busy !== null} onClick={handleAdd}>
          {busy === 'saving' ? 'Testing & adding…' : 'Add API'}
        </button>
        <button type="button" disabled={busy !== null} onClick={handleTest}>
          {busy === 'testing' ? 'Testing…' : 'Test connection'}
        </button>
      </div>
    </section>
  )
}
