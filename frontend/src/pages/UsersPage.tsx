import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import { Banner, type BannerState } from '../components/Banner'
import type { Permissions, User } from '../api/types'

const PERM_COLUMNS: { key: keyof Permissions; label: string }[] = [
  { key: 'viewStatus', label: 'View status' },
  { key: 'triggerSync', label: 'Trigger sync' },
  { key: 'viewConfig', label: 'View config' },
  { key: 'editConfigAPI', label: 'Edit API' },
  { key: 'editConfigDatabase', label: 'Edit database' },
  { key: 'editConfigMaps', label: 'Edit maps' },
  { key: 'editConfigSSO', label: 'Edit SSO' },
]

const emptyPermissions: Permissions = {
  viewStatus: false,
  triggerSync: false,
  viewConfig: false,
  editConfigAPI: false,
  editConfigDatabase: false,
  editConfigMaps: false,
  editConfigSSO: false,
}

export function UsersPage() {
  const [users, setUsers] = useState<User[]>([])
  const [banner, setBanner] = useState<BannerState | null>(null)

  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newSuperuser, setNewSuperuser] = useState(false)
  const [newPermissions, setNewPermissions] = useState<Permissions>(emptyPermissions)
  const [creating, setCreating] = useState(false)

  const load = useCallback(async () => {
    try {
      setUsers(await api.listUsers())
    } catch (err) {
      setBanner({ ok: false, text: `Failed to load users: ${err instanceof ApiError ? err.message : err}` })
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function saveUser(u: User, patch: { isSuperuser?: boolean; permission?: keyof Permissions; value?: boolean }) {
    const body = {
      isSuperuser: patch.isSuperuser ?? u.isSuperuser,
      permissions: patch.permission ? { ...u.permissions, [patch.permission]: patch.value } : u.permissions,
      password: '',
    }

    try {
      await api.patchUser(u.id, body)
      setBanner({ ok: true, text: 'Saved.' })
      await load()
    } catch (err) {
      setBanner({ ok: false, text: `Failed to save: ${err instanceof ApiError ? err.message : err}` })
      await load()
    }
  }

  async function handleDelete(u: User) {
    if (!confirm(`Delete user "${u.username}"?`)) return

    try {
      await api.deleteUser(u.id)
      await load()
    } catch (err) {
      setBanner({ ok: false, text: `Failed to delete: ${err instanceof ApiError ? err.message : err}` })
    }
  }

  async function handleCreate() {
    setCreating(true)

    try {
      await api.createUser({ username: newUsername, password: newPassword, isSuperuser: newSuperuser, permissions: newPermissions })
      setNewUsername('')
      setNewPassword('')
      setNewSuperuser(false)
      setNewPermissions(emptyPermissions)
      setBanner({ ok: true, text: 'User created.' })
      await load()
    } catch (err) {
      setBanner({ ok: false, text: `Failed to create user: ${err instanceof ApiError ? err.message : err}` })
    } finally {
      setCreating(false)
    }
  }

  return (
    <>
      <Banner state={banner} />

      <div className="card">
        <h2>Users</h2>
        <p className="hint">
          Superuser controls user management only &mdash; it does not by itself grant any of the seven
          feature permissions below, and vice versa.
        </p>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <tbody>
              <tr>
                <th>Username</th>
                <th>Superuser</th>
                {PERM_COLUMNS.map((c) => (
                  <th key={c.key}>{c.label}</th>
                ))}
                <th></th>
              </tr>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>{u.username}</td>
                  <td>
                    <input
                      type="checkbox"
                      checked={u.isSuperuser}
                      onChange={(e) => saveUser(u, { isSuperuser: e.target.checked })}
                    />
                  </td>
                  {PERM_COLUMNS.map((c) => (
                    <td key={c.key}>
                      <input
                        type="checkbox"
                        checked={u.permissions[c.key]}
                        onChange={(e) => saveUser(u, { permission: c.key, value: e.target.checked })}
                      />
                    </td>
                  ))}
                  <td>
                    <button type="button" className="danger" onClick={() => handleDelete(u)}>
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card">
        <h2>Add user</h2>
        <label htmlFor="new-username">Username</label>
        <input type="text" id="new-username" value={newUsername} onChange={(e) => setNewUsername(e.target.value)} />
        <label htmlFor="new-password">Password</label>
        <input
          type="password"
          id="new-password"
          value={newPassword}
          onChange={(e) => setNewPassword(e.target.value)}
        />
        <div className="checkbox-row">
          <input
            type="checkbox"
            id="new-superuser"
            checked={newSuperuser}
            onChange={(e) => setNewSuperuser(e.target.checked)}
          />
          <label htmlFor="new-superuser">Superuser (user management)</label>
        </div>
        {PERM_COLUMNS.map((c) => (
          <div className="checkbox-row" key={c.key}>
            <input
              type="checkbox"
              id={`new-${c.key}`}
              checked={newPermissions[c.key]}
              onChange={(e) => setNewPermissions((prev) => ({ ...prev, [c.key]: e.target.checked }))}
            />
            <label htmlFor={`new-${c.key}`}>{c.label}</label>
          </div>
        ))}
        <div className="actions-row">
          <button type="button" className="primary" disabled={creating} onClick={handleCreate}>
            Add user
          </button>
        </div>
      </div>
    </>
  )
}
