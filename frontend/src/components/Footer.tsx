import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { VersionInfo } from '../api/types'

export function Footer() {
  const [info, setInfo] = useState<VersionInfo | null>(null)

  useEffect(() => {
    api.version().then(setInfo).catch(() => {})
  }, [])

  return (
    <footer className="site-footer">
      &copy; 2026 Witt, Nils &middot; Tile-Server-Sync-GO &middot;{' '}
      <a href="https://github.com/Nils-witt/Tile-Server-Sync-GO" target="_blank" rel="noopener noreferrer">
        GitHub
      </a>
      {info && (
        <>
          {' '}
          &middot; {info.version} &middot; {info.commit}
        </>
      )}
    </footer>
  )
}
