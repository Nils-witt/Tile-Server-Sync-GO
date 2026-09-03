export interface BannerState {
  ok: boolean
  text: string
}

export function Banner({ state }: { state: BannerState | null }) {
  if (!state || !state.text) return null

  return <div className={`banner ${state.ok ? 'ok' : 'err'}`}>{state.text}</div>
}
