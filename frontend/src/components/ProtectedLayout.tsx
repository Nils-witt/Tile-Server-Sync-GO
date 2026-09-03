import { Outlet } from 'react-router-dom'
import { TopBar } from './TopBar'
import { Footer } from './Footer'

export function ProtectedLayout() {
  return (
    <>
      <TopBar />
      <main>
        <Outlet />
      </main>
      <Footer />
    </>
  )
}
