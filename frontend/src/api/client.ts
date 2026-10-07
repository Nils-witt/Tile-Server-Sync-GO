import type {
  ConfigGetResponse,
  DatabaseSection,
  ApiSection,
  Me,
  MapDeleteResponse,
  MapSaveResponse,
  MapTarget,
  RemoteMap,
  SecurityLogEntry,
  SectionTestResponse,
  SSOStatus,
  StatusSnapshot,
  SyncResponse,
  VersionInfo,
} from './types'
import { getAccessToken, renewAccessToken } from '../auth/oidc'

/** Thrown by apiFetch on a non-2xx response; message is the server's own {"error": "..."}. */
export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  let token = await getAccessToken()
  let res = await send(path, init, token)

  // A rejected SSO token (expired, or revoked at the provider) gets one silent
  // renewal attempt before the 401 is surfaced.
  if (res.status === 401 && token) {
    token = await renewAccessToken()
    if (token) res = await send(path, init, token)
  }

  const body = await res.json().catch(() => ({}))

  if (!res.ok) {
    throw new ApiError(res.status, body?.error ?? `request failed: ${res.status}`)
  }

  return body as T
}

function send(path: string, init: RequestInit | undefined, token: string | null): Promise<Response> {
  const headers = new Headers(init?.headers)
  if (init?.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (token) headers.set('Authorization', `Bearer ${token}`)

  return fetch(path, { ...init, headers })
}

function postJSON<T>(path: string, body: unknown): Promise<T> {
  return apiFetch<T>(path, { method: 'POST', body: JSON.stringify(body) })
}

function putJSON<T>(path: string, body: unknown): Promise<T> {
  return apiFetch<T>(path, { method: 'PUT', body: JSON.stringify(body) })
}

export const api = {
  me: () => apiFetch<Me>('/api/me'),
  /** Records the just-completed SSO login in the security log; returns the account like me(). */
  ssoLogin: () => postJSON<Me>('/api/sso/login', {}),
  version: () => apiFetch<VersionInfo>('/api/version'),
  ssoStatus: () => apiFetch<SSOStatus>('/api/sso/status'),

  status: () => apiFetch<StatusSnapshot>('/api/status'),
  syncMap: (id: string) => postJSON<SyncResponse>(`/api/maps/${encodeURIComponent(id)}/sync`, {}),

  getAPISection: () => apiFetch<{ api: ApiSection }>('/api/config/api'),
  /** Tests, then saves (only if the test passed), then applies the API section. */
  saveAPISection: (section: ApiSection) => putJSON<ConfigGetResponse>('/api/config/api', { api: section }),
  /** Tests the given (unsaved) API settings; a blank password means the stored one. */
  testAPISection: (section: ApiSection) =>
    postJSON<SectionTestResponse>('/api/config/api/test', { api: section }),
  getDatabaseSection: () => apiFetch<{ database: DatabaseSection }>('/api/config/database'),
  /** Tests, then saves (only if the test passed), then applies the database section. */
  saveDatabaseSection: (section: DatabaseSection) =>
    putJSON<ConfigGetResponse>('/api/config/database', { database: section }),
  /** Tries the given (unsaved) connection settings; a blank password means the stored one. */
  testDatabaseSection: (section: DatabaseSection) =>
    postJSON<SectionTestResponse>('/api/config/database/test', { database: section }),


  listMaps: () => apiFetch<MapTarget[]>('/api/maps'),
  /** The maps the configured tileserve-go API offers, for one-click adding. */
  listRemoteMaps: () => apiFetch<RemoteMap[]>('/api/remote-maps'),
  createMap: (m: MapTarget) => postJSON<MapSaveResponse>('/api/maps', m),
  updateMap: (id: string, m: MapTarget) => putJSON<MapSaveResponse>(`/api/maps/${encodeURIComponent(id)}`, m),
  deleteMap: (id: string) =>
    apiFetch<MapDeleteResponse>(`/api/maps/${encodeURIComponent(id)}`, { method: 'DELETE' }),


  securityLog: () => apiFetch<SecurityLogEntry[]>('/api/security-log'),
}

