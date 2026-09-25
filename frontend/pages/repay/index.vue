<script setup lang="ts">
definePageMeta({ middleware: 'auth' })
useHead({ title: '還款管理 · CreditFlow' })

const { repayableLoans, loans, loadDashboard } = useDashboard()

await useAsyncData('repay-index', async () => {
  if (!loans.value.length) await loadDashboard()
  return true
})

// 有未結清合約時直接進入第一筆，讓 /repay 成為方便的入口
onMounted(async () => {
  const first = repayableLoans.value[0]
  if (first) await navigateTo(`/repay/${first.id}`, { replace: true })
})
</script>

<template>
  <section class="page active-page">
    <div class="container">
      <div class="page-intro">
        <div>
          <span class="section-kicker">REPAYMENT CENTER</span>
          <h1>還款管理</h1>
          <p>清楚掌握每一期還款進度與資金安排</p>
        </div>
      </div>

      <div v-if="repayableLoans.length" class="listing-grid">
        <NuxtLink
          v-for="loan in repayableLoans"
          :key="loan.id"
          class="listing-card"
          :to="`/repay/${loan.id}`"
        >
          <div class="listing-heading">
            <div><strong>{{ loan.id }}</strong><small>{{ loan.product }}</small></div>
            <StatusTag :label="loan.status" :tone="loan.statusTone" />
          </div>
          <div class="listing-row"><span>核貸金額</span><strong>{{ formatNT(loan.amount) }}</strong></div>
          <div class="listing-row"><span>月付金</span><strong>{{ formatNT(loan.monthlyPayment) }}</strong></div>
          <div class="listing-row">
            <span>進度</span>
            <strong>{{ loan.paidInstallments }} / {{ loan.totalInstallments }} 期</strong>
          </div>
        </NuxtLink>
      </div>

      <p v-else class="empty-state">
        目前沒有未結清的合約。
        <NuxtLink class="link-button" to="/apply">申請一筆新貸款</NuxtLink>
      </p>
    </div>
  </section>
</template>
