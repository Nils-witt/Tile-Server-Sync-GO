// These mirror the JSON DTOs defined in internal/webserver (Go) field for
// field — see that package's config.go, maps.go, sso.go,
// security_log.go, status_api.go, and auth.go for the source of truth.

/** Mirrors config.SSOPermissions — granted solely by the user's SSO groups. */
export interface Permissions {
  viewStatus: boolean
  triggerSync: boolean
  viewConfig: boolean
  editConfigApi: boolean
  editConfigDatabase: boolean
  editConfigMaps: boolean
}

export interface Me {
  username: string
  isSuperuser: boolean
  permissions: Permissions
}

export interface ApiSection {
  baseUrl: string
  username: string
  password: string
  token: string
}

export interface DatabaseSection {
  host: string
  /** 0 means the default, 3306. */
  port: number
  user: string
  password: string
  /** Database (schema) name. */
  name: string
  /** Extra driver params in URL query form, e.g. "tls=true"; parseTime=true is always set. */
  params: string
  table: string
  pruneMissing: boolean
  syncOverlays: boolean
  columns: Record<string, string>
}

export interface MapTarget {
  id: string
  name: string
  versions: string[]
  interval: string
  staticColumns: Record<string, string>
  disabled: boolean
}

export interface Config {
  api: ApiSection
  database: DatabaseSection
  maps: MapTarget[]
}

export interface ConfigGetResponse {
  config?: Config
  error?: string
  applied?: boolean
  applyError?: string
}

export interface MapSaveResponse {
  map?: MapTarget
  error?: string
  applied?: boolean
  applyError?: string
  overlayError?: string
}

export interface MapDeleteResponse {
  ok: boolean
  error?: string
  applied?: boolean
  applyError?: string
  objectsDeleted?: number
  objectsError?: string
  overlayError?: string
}

export interface SyncResponse {
  ok: boolean
  synced: number
  error?: string
}

/** Public SSO settings the SPA needs to run the OIDC login itself. */
export interface SSOStatus {
  buttonLabel: string
  issuerUrl: string
  clientId: string
  scopes: string
}

export interface SecurityLogEntry {
  at: string
  eventType: string
  username: string
  remoteAddr: string
  detail: string
}

export interface MapVersionResult {
  mapId: string
  version: string
  synced: number
  err?: string
  at: string
}

export interface StatusSnapshot {
  startedAt: string
  runs: number
  lastRunAt?: string
  lastRunErr?: string
  totalSynced: number
  results: MapVersionResult[]
  logs: string[]
}

export interface VersionInfo {
  version: string
  commit: string
}

