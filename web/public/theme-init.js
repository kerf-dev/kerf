// Applies the saved theme before first paint so dark mode never flashes. A file (not inline) so the
// Content-Security-Policy can stay `script-src 'self'`.
try {
  var t = localStorage.getItem('ro-theme') || 'system'
  var d = t === 'dark' || (t === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', d)
} catch (e) {}
