export default defineNuxtConfig({
  compatibilityDate: '2024-04-03',
  devtools: { enabled: false },
  css: ['~/assets/css/main.css'],
  runtimeConfig: {
    backendInternalUrl: process.env.NUXT_BACKEND_INTERNAL_URL || process.env.BACKEND_INTERNAL_URL || 'http://localhost:8080',
    public: {
      apiBase: process.env.NUXT_PUBLIC_API_BASE || '/api',
      // 展示模式：顯示展示帳號、模擬數據說明與未接金流的入金。
      // 預設關閉，只有 develop compose 以 NUXT_PUBLIC_DEMO_MODE=true 開啟。
      demoMode: false,
    },
  },
  nitro: {
    preset: 'node-server',
  },
})
