// Theme helper: single place that maps settings.theme to the <html> class.
// Switching is instant (CSS variables flip, no reload).
export function syncThemeClass(theme: string): void {
  document.documentElement.classList.toggle('dark', theme === 'dark')
}
