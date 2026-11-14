<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, kg, yuan } from '../api/client.js'

const stock = ref([])
const payables = ref([])
const recon = ref(null)
const error = ref('')

const totals = computed(() => {
  const t = { usable: 0, isolated: 0, production: 0, shipped: 0, metal: 0 }
  for (const s of stock.value) {
    if (s.pile.kind === 'usable') { t.usable += s.weight_g; t.metal += s.metal_mg }
    if (s.pile.kind === 'isolated') t.isolated += s.weight_g
    if (s.pile.kind === 'production') t.production += s.weight_g
    if (s.pile.kind === 'shipped') t.shipped += s.weight_g
  }
  return t
})
const payableTotals = computed(() => {
  let current = 0, pending = 0, paid = 0
  for (const p of payables.value) {
    if (p.is_current) current += p.amount_cents
    if (p.status === 'paid') paid += p.amount_cents
    if (p.is_current && (p.status === 'calculated' || p.status === 'held')) pending += p.amount_cents
  }
  return { current, pending, paid }
})

onMounted(async () => {
  try {
    const [s, p, r] = await Promise.all([
      api.get('/api/stock'), api.get('/api/payables'), api.get('/api/reconciliation')
    ])
    stock.value = s; payables.value = p; recon.value = r
  } catch (e) { error.value = e.message }
})
</script>

<template>
  <div>
    <h1>总览</h1>
    <p class="lead">采购、料场与财务在同一套逐车批次上核对数量与金额，而不是各自维护互不相认的总数。</p>
    <div v-if="error" class="err">{{ error }}</div>

    <div class="statgrid">
      <div class="stat">
        <div class="label">可用堆库存</div>
        <div class="value">{{ kg(totals.usable) }} kg</div>
        <div class="hint">仅已批准等级、非隔离料</div>
      </div>
      <div class="stat">
        <div class="label">隔离料库存</div>
        <div class="value">{{ kg(totals.isolated) }} kg</div>
        <div class="hint">不得通过混料变为可用</div>
      </div>
      <div class="stat">
        <div class="label">投炉/在产</div>
        <div class="value">{{ kg(totals.production) }} kg</div>
        <div class="hint">已投入生产，去向不可撤销</div>
      </div>
      <div class="stat">
        <div class="label">已发运</div>
        <div class="value">{{ kg(totals.shipped) }} kg</div>
        <div class="hint">出库形成的来源关系</div>
      </div>
    </div>

    <div class="grid cols-2">
      <div class="panel">
        <h2>当前应付（各车次最新版本）</h2>
        <dl class="kv">
          <dt>当前有效合计</dt><dd class="num">¥ {{ yuan(payableTotals.current) }}</dd>
          <dt>待财务批准</dt><dd class="num">¥ {{ yuan(payableTotals.pending) }}</dd>
          <dt>已付（历史版本）</dt><dd class="num">¥ {{ yuan(payableTotals.paid) }}</dd>
        </dl>
        <p class="muted" style="font-size:12px;margin-top:12px">
          复检或复称不会直接改可付款状态；财务先看到新旧差额，再批准调整。
        </p>
      </div>
      <div class="panel" v-if="recon">
        <h2>守恒自查</h2>
        <dl class="kv">
          <dt>入库重量</dt><dd class="num">{{ kg(recon.totals.intake_g) }} kg</dd>
          <dt>在库 + 发运 + 明确损耗</dt>
          <dd class="num">{{ kg(recon.totals.on_hand_g + recon.totals.shipped_g + recon.totals.loss_g) }} kg</dd>
          <dt>重量尾差</dt>
          <dd class="num" :style="{ color: recon.totals.gap_g === 0 ? 'var(--accent)' : 'var(--danger)' }">
            {{ kg(recon.totals.gap_g) }} kg
          </dd>
          <dt>金属量尾差</dt>
          <dd class="num" :style="{ color: recon.totals.gap_metal_mg === 0 ? 'var(--accent)' : 'var(--danger)' }">
            {{ (recon.totals.gap_metal_mg / 1e6).toFixed(3) }} kg
          </dd>
        </dl>
        <p class="muted" style="font-size:12px;margin-top:12px">
          水分干燥损失只减重量、不减金属；加工损耗按比例同时减重与减金属。
        </p>
      </div>
    </div>

    <div class="panel">
      <h2>按物理堆位</h2>
      <table>
        <thead><tr><th>堆位</th><th>类型</th><th class="num">重量 (kg)</th><th class="num">金属量 (kg)</th><th>来源</th></tr></thead>
        <tbody>
          <tr v-for="s in stock" :key="s.pile.id">
            <td>{{ s.pile.name }}</td>
            <td><span class="badge gray">{{ s.pile.kind }}</span></td>
            <td class="num">{{ kg(s.weight_g) }}</td>
            <td class="num">{{ (s.metal_mg / 1e6).toFixed(3) }}</td>
            <td>
              <span v-if="!s.origins.length" class="muted">—</span>
              <span v-for="o in s.origins" :key="o.trip_id" class="badge" style="margin-right:4px">
                {{ o.batch_no }} · {{ kg(o.weight_g) }}kg
              </span>
              <span v-if="s.unknown_share" class="badge warn">来源比例未知</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
