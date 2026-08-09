export default defineNuxtConfig({
  compatibilityDate: '2024-04-03',
  devtools: { enabled: false },
  css: ['~/assets/css/main.css'],
  runtimeConfig: {
    backendInternalUrl: process.env.NUXT_BACKEND_INTERNAL_URL || process.env.BACKEND_INTERNAL_URL || 'http://localhost:8080',
    public: {
      apiBase: process.env.NUXT_PUBLIC_API_BASE || '/api',
    },
  },
  nitro: {
    preset: 'node-server',
  },
})
