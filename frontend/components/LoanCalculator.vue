<script setup lang="ts">
/** 首頁的貸款試算機。帶入申請時透過 query 傳遞金額與期數。 */
const calculator = reactive({ amount: 500000, term: 36, rate: 4.88 })

const rateOptions = [
  { label: 'A 級｜年利率 4.88%', value: 4.88 },
  { label: 'B 級｜年利率 6.80%', value: 6.8 },
  { label: 'C 級｜年利率 9.60%', value: 9.6 },
]

const payment = computed(() => pmt(calculator.amount, calculator.rate, calculator.term))
const total = computed(() => payment.value * calculator.term)
const interest = computed(() => total.value - calculator.amount)
const fee = computed(() => calculator.amount * 0.015)
</script>

<template>
  <div class="calculator-card">
    <div class="card-heading">
      <div><span class="section-kicker">QUICK CALCULATOR</span><h2>貸款試算</h2></div>
      <span class="heading-badge">即時估算</span>
    </div>

    <div class="range-field">
      <div class="range-label"><label for="calc-amount">借款金額</label><strong>{{ formatNT(calculator.amount) }}</strong></div>
      <input id="calc-amount" v-model.number="calculator.amount" class="range-input" type="range" min="50000" max="3000000" step="10000">
      <div class="range-hints"><span>50,000</span><span>3,000,000</span></div>
    </div>

    <div class="range-field">
      <div class="range-label"><label for="calc-term">借款期數</label><strong>{{ calculator.term }} 期</strong></div>
      <input id="calc-term" v-model.number="calculator.term" class="range-input" type="range" min="6" max="84" step="6">
      <div class="range-hints"><span>6 期</span><span>84 期</span></div>
    </div>

    <div class="field compact-field">
      <label for="calc-grade">信用等級</label>
      <select id="calc-grade" v-model.number="calculator.rate">
        <option v-for="option in rateOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
      </select>
    </div>

    <div class="calculator-output">
      <div class="output-row highlight"><span>每月應繳</span><strong>{{ formatNT(payment) }}</strong></div>
      <div class="output-row"><span>總利息支出</span><strong>{{ formatNT(interest) }}</strong></div>
      <div class="output-row"><span>總還款金額</span><strong>{{ formatNT(total) }}</strong></div>
      <div class="output-row"><span>開辦費（1.5%，內含）</span><strong>{{ formatNT(fee) }}</strong></div>
    </div>

    <NuxtLink
      class="btn primary full"
      :to="{ path: '/apply', query: { amount: calculator.amount, term: calculator.term } }"
    >
      帶入資料去申請 <span>→</span>
    </NuxtLink>
  </div>
</template>
