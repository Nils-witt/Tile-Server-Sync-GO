import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import { Banner, type BannerState } from '../components/Banner'
import type { SecurityLogEntry } from '../api/types'

export function SecurityLogPage() {
  const [entries, setEntries] = useState<SecurityLogEntry[]>([])
  const [banner, setBanner] = useState<BannerState | null>(null)

  const load = useCallback(async () => {
    try {
      setEntries(await api.securityLog())
    } catch (err) {
      setBanner({ ok: false, text: `Failed to load security log: ${err instanceof ApiError ? err.message : err}` })
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <>
      <Banner state={banner} />

      <div className="card">
        <h2>Security log</h2>
        <p className="hint">
          Logins (local and SSO, successful and failed), logouts, user-account changes, and config saves,
          newest first. This is an audit trail, not a live view &mdash; use the Refresh button to see new
          entries.
        </p>
        <div className="actions-row">
          <button type="button" onClick={load}>
            Refresh
          </button>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <tbody>
              <tr>
                <th>Time</th>
                <th>Event</th>
                <th>Username</th>
                <th>Remote address</th>
                <th>Detail</th>
              </tr>
              {entries.map((e, i) => (
                <tr key={i}>
                  <td>{e.at}</td>
                  <td>{e.eventType}</td>
                  <td>{e.username}</td>
                  <td>{e.remoteAddr}</td>
                  <td>{e.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </>
  )
}
