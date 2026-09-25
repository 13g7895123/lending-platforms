<script setup lang="ts">
/**
 * 全站版面：導覽列、頁尾，以及跨頁共用的 toast 與對話框容器。
 * 頁面本身只需關注自己的內容。
 */
</script>

<template>
  <div class="app-shell">
    <a class="skip-link" href="#main-content">跳到主要內容</a>

    <AppHeader />

    <main id="main-content" tabindex="-1">
      <slot />
    </main>

    <AppFooter />

    <ModalDialog />
    <ToastMessage />
  </div>
</template>
