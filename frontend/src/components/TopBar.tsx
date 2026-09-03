import { NavLink, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { ThemeToggle } from './ThemeToggle'

export function TopBar() {
  const { me, logout } = useAuth()
  const navigate = useNavigate()

  async function handleLogout(e: React.MouseEvent) {
    e.preventDefault()
    await logout()
    navigate('/login')
  }

  return (
    <div className="topbar">
      <span className="brand">Tile-Server-Sync-GO</span>
      <nav>
        <NavLink to="/" end>
          Status
        </NavLink>
        {me?.permissions.viewConfig && <NavLink to="/config">Config</NavLink>}
        {me?.isSuperuser && <NavLink to="/users">Users</NavLink>}
        {me?.isSuperuser && <NavLink to="/security-log">Security log</NavLink>}
      </nav>
      <nav id="account-nav">
        {me && (
          <>
            <span>{me.username}</span>
            <a href="#" onClick={handleLogout}>
              Logout
            </a>
          </>
        )}
      </nav>
      <ThemeToggle />
    </div>
  )
}
