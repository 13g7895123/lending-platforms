<script setup lang="ts">
const { isAuthenticated } = useAuth()

useHead({ title: 'CreditFlow 信達金融｜智慧借貸平台' })

const features = [
  { icon: '✦', tone: 'cyan', title: '透明定價', body: '利率、費用與還款明細一次掌握，沒有模糊不清的隱藏成本。' },
  { icon: '⌁', tone: 'blue', title: '一致的風控標準', body: '依信用評分與負債比套用公開的授信規則，每個決策都有依據，也讓好信用更有價值。' },
  { icon: '↗', tone: 'green', title: '靈活資金', body: '媒合多元出借資金，依需求找到合適的額度與期數。' },
  { icon: '◌', tone: 'purple', title: '全程線上', body: '申請、審核、撥款與還款，使用一個平台就能輕鬆管理。' },
]
</script>

<template>
  <section class="page active-page">
    <div class="container hero-grid">
      <div class="hero-copy">
        <span class="eyebrow"><i class="pulse-dot" />智慧借貸，讓資金流動更有價值</span>
        <h1>把每一次資金需求，<em>變成前進的力量</em></h1>
        <p>CreditFlow 以透明定價、即時風控與多元資金來源，為借款人與出借人建立更值得信賴的金融連結。</p>

        <div class="hero-actions">
          <NuxtLink v-if="isAuthenticated" class="btn primary" to="/apply">
            立即試算並申請 <span>→</span>
          </NuxtLink>
          <NuxtLink v-else class="btn primary" to="/login?mode=register">
            註冊並開始申請 <span>→</span>
          </NuxtLink>
          <NuxtLink class="btn" to="/market">我要出借投資</NuxtLink>
        </div>
      </div>

      <LoanCalculator />
    </div>

    <div class="container feature-grid">
      <article v-for="feature in features" :key="feature.title" class="feature-card">
        <span class="feature-icon" :class="feature.tone">{{ feature.icon }}</span>
        <h3>{{ feature.title }}</h3>
        <p>{{ feature.body }}</p>
      </article>
    </div>
  </section>
</template>
