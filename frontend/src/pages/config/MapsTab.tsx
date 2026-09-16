import { useEffect, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { Banner, type BannerState } from '../../components/Banner'
import { MapCard } from './MapCard'
import type { MapTarget } from '../../api/types'

interface Row {
  key: string
  map: MapTarget
  persisted: boolean
  openByDefault?: boolean
}

const emptyMap: MapTarget = { id: '', name: '', versions: [], interval: '', staticColumns: {}, disabled: false }

export function MapsTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigMaps
  const [rows, setRows] = useState<Row[]>([])
  const [banner, setBanner] = useState<BannerState | null>(null)

  useEffect(() => {
    api
      .listMaps()
      .then((maps) => setRows((maps || []).map((m) => ({ key: m.id, map: m, persisted: true }))))
      .catch((err) => setBanner({ ok: false, text: `Failed to load maps: ${err instanceof ApiError ? err.message : err}` }))
  }, [])

  function handleAdd() {
    setRows((prev) => [...prev, { key: `new-${Date.now()}`, map: emptyMap, persisted: false, openByDefault: true }])
  }

  function handleRemoved(key: string) {
    setRows((prev) => prev.filter((r) => r.key !== key))
  }

  return (
    <section className="card">
      <h2>Maps</h2>
      <p className="hint">Each map is fetched independently, on its own optional interval.</p>
      <Banner state={banner} />

      <div>
        {rows.map((row) => (
          <MapCard
            key={row.key}
            initial={row.map}
            persisted={row.persisted}
            openByDefault={row.openByDefault}
            disabled={disabled}
            onRemoved={() => handleRemoved(row.key)}
          />
        ))}
      </div>

      <div className="actions-row">
        <button type="button" disabled={disabled} onClick={handleAdd}>
          + Add map
        </button>
      </div>
    </section>
  )
}
