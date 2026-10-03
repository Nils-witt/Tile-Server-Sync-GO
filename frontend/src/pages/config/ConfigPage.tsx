import { Navigate, NavLink, Outlet, Route, Routes } from 'react-router-dom'
import { ApiTab } from './ApiTab'
import { DatabaseTab } from './DatabaseTab'
import { MapsTab } from './MapsTab'

function ConfigShell() {
  return (
    <>
      <p className="hint" style={{ marginBottom: '1.1rem' }}>
        Saving writes to the config database and applies it to the running process immediately &mdash;
        API credentials, database settings, and maps (including each map's own interval) all take
        effect right away, no restart needed. To run a sync immediately instead of waiting for a map's
        interval, use the per-map Sync button on the <a href="/">status page</a>.
      </p>

      <div className="tabs" role="tablist">
        <NavLink to="/config/api" className="tab-btn" role="tab">
          API
        </NavLink>
        <NavLink to="/config/database" className="tab-btn" role="tab">
          Database
        </NavLink>
        <NavLink to="/config/maps" className="tab-btn" role="tab">
          Maps
        </NavLink>
      </div>

      <Outlet />
    </>
  )
}

export function ConfigPage() {
  return (
    <Routes>
      <Route element={<ConfigShell />}>
        <Route index element={<Navigate to="api" replace />} />
        <Route path="api" element={<ApiTab />} />
        <Route path="database" element={<DatabaseTab />} />
        <Route path="maps" element={<MapsTab />} />
      </Route>
    </Routes>
  )
}
