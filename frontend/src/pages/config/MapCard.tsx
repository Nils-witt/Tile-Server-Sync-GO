import { useMemo, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { Banner, type BannerState } from '../../components/Banner'
import type { MapTarget } from '../../api/types'

function parseStaticColumns(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim()
    if (!line) continue
    const i = line.indexOf('=')
    if (i < 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

function staticColumnsText(cols: Record<string, string>): string {
  return Object.entries(cols)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

interface Props {
  initial: MapTarget
  persisted: boolean
  openByDefault?: boolean
  disabled: boolean
  onRemoved: () => void
}

export function MapCard({ initial, persisted: initialPersisted, openByDefault, disabled, onRemoved }: Props) {
  const [persisted, setPersisted] = useState(initialPersisted)
  const [id, setId] = useState(initial.id)
  const [name, setName] = useState(initial.name)
  const [versionsText, setVersionsText] = useState((initial.versions || []).join(', '))
  const [interval, setInterval_] = useState(initial.interval)
  const [colsText, setColsText] = useState(staticColumnsText(initial.staticColumns || {}))
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [saving, setSaving] = useState(false)
  const [removing, setRemoving] = useState(false)

  const versions = useMemo(
    () => versionsText.split(',').map((v) => v.trim()).filter(Boolean),
    [versionsText],
  )

  const summaryId = name.trim() || id.trim() || '(new map)'
  const summaryMeta = `${versions.length === 1 ? '1 version' : `${versions.length} versions`} · ${interval.trim() ? `every ${interval.trim()}` : 'one-shot'}`

  async function handleSave(e: React.MouseEvent) {
    e.preventDefault()
    const trimmedID = id.trim()
    const payload: MapTarget = {
      id: trimmedID,
      name: name.trim(),
      versions,
      interval: interval.trim(),
      staticColumns: parseStaticColumns(colsText),
    }

    setSaving(true)

    try {
      const res = persisted ? await api.updateMap(trimmedID, payload) : await api.createMap(payload)

      if (res.map) {
        setPersisted(true)
        if (!res.applied) {
          setBanner({ ok: false, text: `Saved, but failed to apply to the running process: ${res.applyError}` })
        } else if (res.overlayError) {
          setBanner({ ok: false, text: `Saved and applied, but failed to sync EDP overlay: ${res.overlayError}` })
        } else {
          setBanner({ ok: true, text: 'Saved and applied to the running process.' })
        }
      } else {
        setBanner({ ok: false, text: `Not saved: ${res.error}` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Failed to save map: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setSaving(false)
    }
  }

  async function handleRemove(e: React.MouseEvent) {
    e.preventDefault()

    if (!persisted) {
      onRemoved()
      return
    }

    setRemoving(true)

    try {
      const res = await api.deleteMap(id.trim())
      if (res.ok) {
        onRemoved()
      } else {
        setBanner({ ok: false, text: `Failed to remove map: ${res.error}` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Failed to remove map: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setRemoving(false)
    }
  }

  return (
    <details className="map-card" open={openByDefault}>
      <summary>
        <span className="map-summary-id">{summaryId}</span>
        <span className="map-summary-meta">{summaryMeta}</span>
      </summary>
      <div className="map-body">
        <label>Map ID</label>
        <input type="text" value={id} disabled={persisted || disabled} onChange={(e) => setId(e.target.value)} />

        <label>Name (human-readable; required to sync this map into EDP's overlay list)</label>
        <input type="text" value={name} disabled={disabled} onChange={(e) => setName(e.target.value)} />

        <label>Versions (comma-separated, e.g. current, 3, 4)</label>
        <input type="text" value={versionsText} disabled={disabled} onChange={(e) => setVersionsText(e.target.value)} />

        <label>Interval (Go duration, e.g. "5m"; empty = sync once, no automatic repeat)</label>
        <input type="text" placeholder="5m" value={interval} disabled={disabled} onChange={(e) => setInterval_(e.target.value)} />

        <label>Static columns (one key=value per line)</label>
        <textarea rows={3} value={colsText} disabled={disabled} onChange={(e) => setColsText(e.target.value)} />

        <div className="actions-row">
          <button type="button" className="primary" disabled={disabled || saving} onClick={handleSave}>
            Save
          </button>
          <button type="button" className="danger" disabled={disabled || removing} onClick={handleRemove}>
            Remove map
          </button>
        </div>
        <Banner state={banner} />
      </div>
    </details>
  )
}
