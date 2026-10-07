import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useAuth } from '../../auth/AuthContext'
import { Banner, type BannerState } from '../../components/Banner'
import { MapCard } from './MapCard'
import type { ApiTarget, MapTarget, RemoteMap } from '../../api/types'

/** What a one-click add creates; everything stays editable on the map's card afterwards. */
function newMapFrom(remote: RemoteMap, apiId: string): MapTarget {
  return {
    id: remote.id,
    apiId,
    name: remote.name,
    versions: ['current'],
    interval: '1h',
    staticColumns: {},
    disabled: false,
  }
}

function errorText(err: unknown): string {
  return err instanceof ApiError ? err.message : String(err)
}

export function MapsTab() {
  const { me } = useAuth()
  const disabled = !me?.permissions.editConfigMaps
  const [maps, setMaps] = useState<MapTarget[]>([])
  const [apis, setApis] = useState<ApiTarget[]>([])
  const [banner, setBanner] = useState<BannerState | null>(null)

  useEffect(() => {
    Promise.all([api.listMaps(), api.listAPIs()])
      .then(([mapList, apiList]) => {
        setMaps(mapList || [])
        setApis(apiList || [])
      })
      .catch((err) => setBanner({ ok: false, text: `Failed to load maps: ${errorText(err)}` }))
  }, [])

  function handleAdded(m: MapTarget) {
    setMaps((prev) => [...prev, m])
  }

  function handleRemoved(id: string) {
    setMaps((prev) => prev.filter((m) => m.id !== id))
  }

  const configuredIds = useMemo(() => new Set(maps.map((m) => m.id)), [maps])

  return (
    <>
      <section className="card">
        <h2>Maps</h2>
        <p className="hint">Each map is fetched independently, on its own optional interval.</p>
        <Banner state={banner} />

        {maps.length === 0 && <p className="hint">No maps configured yet — add one from the list below.</p>}
        <div>
          {maps.map((m) => (
            <MapCard key={m.id} initial={m} apis={apis} disabled={disabled} onRemoved={() => handleRemoved(m.id)} />
          ))}
        </div>
      </section>

      {!disabled && <AvailableMaps apis={apis} configuredIds={configuredIds} onAdded={handleAdded} />}
    </>
  )
}

interface AvailableMapsProps {
  apis: ApiTarget[]
  configuredIds: Set<string>
  onAdded: (m: MapTarget) => void
}

/** The maps the selected tileserve-go API offers, each addable with one click. */
function AvailableMaps({ apis, configuredIds, onAdded }: AvailableMapsProps) {
  const [selectedApiId, setSelectedApiId] = useState('')
  const apiId = apis.some((a) => a.id === selectedApiId) ? selectedApiId : (apis[0]?.id ?? '')
  const [remote, setRemote] = useState<RemoteMap[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [filter, setFilter] = useState('')
  const [adding, setAdding] = useState<string | null>(null)
  const [banner, setBanner] = useState<BannerState | null>(null)

  const load = useCallback(async () => {
    if (!apiId) {
      setRemote(null)
      return
    }

    setLoading(true)
    try {
      setRemote((await api.listRemoteMaps(apiId)) || [])
      setBanner(null)
    } catch (err) {
      setRemote(null)
      setBanner({ ok: false, text: `Failed to load maps from the API: ${errorText(err)}` })
    } finally {
      setLoading(false)
    }
  }, [apiId])

  useEffect(() => {
    void load()
  }, [load])

  async function handleAdd(r: RemoteMap) {
    setAdding(r.id)
    try {
      const res = await api.createMap(newMapFrom(r, apiId))
      if (!res.map) {
        setBanner({ ok: false, text: `Failed to add "${r.name}": ${res.error}` })
        return
      }

      onAdded(res.map)
      if (!res.applied) {
        setBanner({ ok: false, text: `Added "${r.name}", but failed to apply to the running process: ${res.applyError}` })
      } else if (res.overlayError) {
        setBanner({ ok: false, text: `Added "${r.name}", but failed to sync EDP overlay: ${res.overlayError}` })
      } else {
        setBanner({ ok: true, text: `Added "${r.name}".` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Failed to add "${r.name}": ${errorText(err)}` })
    } finally {
      setAdding(null)
    }
  }

  const needle = filter.trim().toLowerCase()
  const shown = (remote || []).filter(
    (r) => !needle || r.name.toLowerCase().includes(needle) || r.description.toLowerCase().includes(needle) || r.id.includes(needle),
  )

  return (
    <section className="card">
      <h2>Available maps</h2>
      <p className="hint">
        Maps offered by the selected API. Adding one syncs its current version every hour; adjust it on its card above
        afterwards.
      </p>
      <Banner state={banner} />

      {apis.length === 0 && <p className="hint">Add an API on the API tab first.</p>}
      <div className="actions-row" style={{ marginTop: 0, marginBottom: '0.6rem' }}>
        <select aria-label="API" style={{ width: 'auto' }} value={apiId} disabled={apis.length === 0} onChange={(e) => setSelectedApiId(e.target.value)}>
          {apis.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name || a.id}
            </option>
          ))}
        </select>
        <input type="text" placeholder="Filter by name or description" value={filter} onChange={(e) => setFilter(e.target.value)} />
        <button type="button" disabled={loading || !apiId} onClick={() => void load()}>
          {loading ? 'Loading…' : 'Refresh'}
        </button>
      </div>

      {remote && remote.length === 0 && <p className="hint">The API offers no maps to this account.</p>}
      {remote && remote.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Current version</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => {
              const added = configuredIds.has(r.id)
              return (
                <tr key={r.id}>
                  <td title={r.id}>{r.name || r.id}</td>
                  <td>{r.description}</td>
                  <td>{r.currentVersion}</td>
                  <td>
                    <button
                      type="button"
                      className={added ? undefined : 'primary'}
                      disabled={added || adding !== null}
                      onClick={() => void handleAdd(r)}
                    >
                      {added ? 'Added' : adding === r.id ? 'Adding…' : 'Add'}
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </section>
  )
}
