import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { Banner, type BannerState } from '../components/Banner'
import type { StatusSnapshot } from '../api/types'

const POLL_MS = 10_000

function formatAt(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).replace(',', '')
}

export function StatusPage() {
  const { me } = useAuth()
  const [snapshot, setSnapshot] = useState<StatusSnapshot | null>(null)
  const [banner, setBanner] = useState<BannerState | null>(null)
  const [syncing, setSyncing] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    try {
      setSnapshot(await api.status())
    } catch (err) {
      setBanner({ ok: false, text: err instanceof ApiError ? `Failed to load status: ${err.message}` : `Failed to load status: ${err}` })
    }
  }, [])

  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    void load()
    timer.current = setInterval(load, POLL_MS)
    return () => {
      if (timer.current) clearInterval(timer.current)
    }
  }, [load])

  async function handleSync(mapId: string) {
    setSyncing((prev) => new Set(prev).add(mapId))
    setBanner({ ok: true, text: `Syncing ${mapId}…` })

    try {
      const res = await api.syncMap(mapId)
      if (res.ok) {
        setBanner({ ok: true, text: `Synced ${mapId}: ${res.synced} object(s).` })
        await load()
      } else {
        setBanner({ ok: false, text: `Sync failed for ${mapId}: ${res.error || 'unknown error'}` })
      }
    } catch (err) {
      setBanner({ ok: false, text: `Sync failed for ${mapId}: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setSyncing((prev) => {
        const next = new Set(prev)
        next.delete(mapId)
        return next
      })
    }
  }

  if (!snapshot) {
    return (
      <>
        <Banner state={banner} />
        <p className="hint">Loading…</p>
      </>
    )
  }

  return (
    <>
      <Banner state={banner} />

      <div className="stat-grid">
        <div className="stat-card">
          <div className="label">Started</div>
          <div className="value" style={{ fontSize: '1.1rem' }}>
            {formatAt(snapshot.startedAt)}
          </div>
        </div>
        <div className="stat-card">
          <div className="label">Runs</div>
          <div className="value">{snapshot.runs}</div>
        </div>
        <div className="stat-card">
          <div className="label">Last run</div>
          {!snapshot.lastRunAt ? (
            <div className="value" style={{ fontSize: '1.1rem' }}>
              never
            </div>
          ) : (
            <>
              <div className="value" style={{ fontSize: '1.1rem' }}>
                {formatAt(snapshot.lastRunAt)}
              </div>
              <div className="sub">
                {snapshot.lastRunErr ? <span className="badge err">error</span> : <span className="badge ok">ok</span>}
              </div>
            </>
          )}
        </div>
        <div className="stat-card">
          <div className="label">Total objects synced</div>
          <div className="value">{snapshot.totalSynced}</div>
        </div>
      </div>

      {snapshot.lastRunErr && (
        <div className="card" style={{ borderColor: 'var(--danger)', background: 'var(--danger-bg)' }}>
          <strong style={{ color: 'var(--danger)' }}>Last run error:</strong> {snapshot.lastRunErr}
        </div>
      )}

      <div className="card">
        <h2>Last result per map/version</h2>
        {snapshot.results.length === 0 ? (
          <p className="hint">No syncs yet.</p>
        ) : (
          <table>
            <tbody>
              <tr>
                <th>Map</th>
                <th>Version</th>
                <th>Synced</th>
                <th>At</th>
                <th>Status</th>
                <th></th>
              </tr>
              {snapshot.results.map((r) => (
                <tr key={`${r.mapId}/${r.version}`}>
                  <td>{r.mapId}</td>
                  <td>{r.version}</td>
                  <td>{r.synced}</td>
                  <td>{formatAt(r.at)}</td>
                  <td>
                    {r.err ? (
                      <span className="badge err" title={r.err}>
                        error
                      </span>
                    ) : (
                      <span className="badge ok">ok</span>
                    )}
                  </td>
                  <td>
                    {me?.permissions.triggerSync && (
                      <button
                        type="button"
                        className="sync-map-btn"
                        disabled={syncing.has(r.mapId)}
                        onClick={() => handleSync(r.mapId)}
                      >
                        {syncing.has(r.mapId) ? 'Syncing…' : 'Sync'}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="card">
        <details className="log-card" open>
          <summary>Recent log output</summary>
          <pre className="log">{snapshot.logs.join('')}</pre>
        </details>
      </div>
    </>
  )
}
