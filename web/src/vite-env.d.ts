/// <reference types="vite/client" />

// vite/client declares the ambient types the SPA relies on: `*.css` side-effect imports (so
// `import "./styles/base.css"` type-checks), `import.meta.env`, and the asset-url import forms.
// Without this reference tsc --noEmit fails on the first CSS import, which is why it is committed
// rather than generated.
