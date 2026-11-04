import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    ignores: [
      'dist',
      'public/mockServiceWorker.js',
      'public/theme-init.js',
      'src/api/schema.d.ts',
      'playwright-report',
      'test-results',
    ],
  },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: { ecmaVersion: 2022, globals: { ...globals.browser, ...globals.node } },
    plugins: { 'react-hooks': reactHooks, 'react-refresh': reactRefresh },
    rules: {
      // Classic hook rules; the React Compiler rules in v7's preset do not apply (no compiler in this build).
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
  {
    files: [
      'src/components/ui/**',
      'src/lib/**',
      'src/main.tsx',
      'src/components/theme-provider.tsx',
      'src/components/domain.tsx',
      'src/components/states.tsx',
      'src/components/layout/**',
      'src/pages/workload/history.tsx',
    ],
    rules: { 'react-refresh/only-export-components': 'off' },
  },
)
