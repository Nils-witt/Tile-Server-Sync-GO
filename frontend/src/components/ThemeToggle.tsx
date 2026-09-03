import { useTheme } from '../theme/ThemeContext'

export function ThemeToggle({ standalone }: { standalone?: boolean }) {
  const { theme, toggle } = useTheme()

  return (
    <button
      type="button"
      className={standalone ? 'theme-toggle standalone' : 'theme-toggle'}
      aria-label="Toggle dark mode"
      onClick={toggle}
    >
      {theme === 'dark' ? 'Light mode' : 'Dark mode'}
    </button>
  )
}
