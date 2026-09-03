import type {
  Config,
  ConfigGetResponse,
  DatabaseSection,
  ApiSection,
  Me,
  MapDeleteResponse,
  MapSaveResponse,
  MapTarget,
  Permissions,
  SecurityLogEntry,
  SetupStatus,
  SSOConfig,
  SSOStatus,
  StatusSnapshot,
  SyncResponse,
  User,
  VersionInfo,
} from './types'

/** Thrown by apiFetch on a non-2xx response; message is the server's own {"error": "..."}. */
export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json', ...init.headers } : init?.headers,
  })

  const body = await res.json().catch(() => ({}))

  if (!res.ok) {
    throw new ApiError(res.status, body?.error ?? `request failed: ${res.status}`)
  }

  return body as T
}

function postJSON<T>(path: string, body: unknown): Promise<T> {
  return apiFetch<T>(path, { method: 'POST', body: JSON.stringify(body) })
}

function putJSON<T>(path: string, body: unknown): Promise<T> {
  return apiFetch<T>(path, { method: 'PUT', body: JSON.stringify(body) })
}

export const api = {
  me: () => apiFetch<Me>('/api/me'),
  setupStatus: () => apiFetch<SetupStatus>('/api/setup-status'),
  version: () => apiFetch<VersionInfo>('/api/version'),
  ssoStatus: () => apiFetch<SSOStatus>('/api/sso/status'),

  login: (username: string, password: string) => postJSON<Me>('/api/login', { username, password }),
  setup: (username: string, password: string) => postJSON<Me>('/api/setup', { username, password }),
  logout: () => postJSON<{ ok: boolean }>('/api/logout', {}),

  status: () => apiFetch<StatusSnapshot>('/api/status'),
  syncMap: (id: string) => postJSON<SyncResponse>(`/api/maps/${encodeURIComponent(id)}/sync`, {}),

  getConfig: () => apiFetch<ConfigGetResponse>('/api/config'),
  getAPISection: () => apiFetch<{ api: ApiSection }>('/api/config/api'),
  saveAPISection: (section: ApiSection) => putJSON<ConfigGetResponse>('/api/config/api', { api: section }),
  getDatabaseSection: () => apiFetch<{ database: DatabaseSection }>('/api/config/database'),
  saveDatabaseSection: (section: DatabaseSection) =>
    putJSON<ConfigGetResponse>('/api/config/database', { database: section }),

  getSSOConfig: () => apiFetch<SSOConfig>('/api/config/sso'),
  saveSSOConfig: (cfg: SSOConfig) => putJSON<SSOConfig>('/api/config/sso', cfg),

  listMaps: () => apiFetch<MapTarget[]>('/api/maps'),
  createMap: (m: MapTarget) => postJSON<MapSaveResponse>('/api/maps', m),
  updateMap: (id: string, m: MapTarget) => putJSON<MapSaveResponse>(`/api/maps/${encodeURIComponent(id)}`, m),
  deleteMap: (id: string) =>
    apiFetch<MapDeleteResponse>(`/api/maps/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  listUsers: () => apiFetch<User[]>('/api/users'),
  createUser: (u: { username: string; password: string; isSuperuser: boolean; permissions: Permissions }) =>
    postJSON<User>('/api/users', u),
  patchUser: (id: number, patch: { password: string; isSuperuser: boolean; permissions: Permissions }) =>
    apiFetch<User>(`/api/users/${id}`, { method: 'PATCH', body: JSON.stringify(patch), headers: { 'Content-Type': 'application/json' } }),
  deleteUser: (id: number) => apiFetch<{ ok: boolean }>(`/api/users/${id}`, { method: 'DELETE' }),

  securityLog: () => apiFetch<SecurityLogEntry[]>('/api/security-log'),
}

export type { Config }
