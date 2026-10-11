<script setup lang="ts">
const { demoMode } = useRuntimeConfig().public
</script>

<template>
  <footer class="site-footer">
    <div class="container footer-inner">
      <span>© 2026 CreditFlow 信達金融科技股份有限公司<template v-if="demoMode"> · 展示資料皆為模擬數據</template></span>
    </div>
  </footer>
</template>
