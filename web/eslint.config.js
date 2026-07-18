import { createRequire } from 'node:module'

const require = createRequire(
  new URL('./tools/lint/package.json', import.meta.url),
)
const eslint = require('@eslint/js')
const reactHooks = require('eslint-plugin-react-hooks')
const reactRefresh = require('eslint-plugin-react-refresh').default
const tseslint = require('typescript-eslint')

export default tseslint.config(
  { ignores: ['src/api/schema.ts'] },
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks, 'react-refresh': reactRefresh },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
    },
  },
)
