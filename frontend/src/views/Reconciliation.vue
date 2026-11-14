<script setup>
import { ref, onMounted } from 'vue'
import { api, kg, yuan } from '../api/client.js'

const r = ref(null)
const error = ref('')
onMounted(async () => {
  try { r.value = await api.get('/api/reconciliation') } catch (e) { error.value = e.message }
})
const mt = (mg) => (mg / 1e6).toFixed(3)
</script>

<template>
  <div>
    <h1>数量与金额核对</h1>
    <p class="lead">逐车核对入库、在库、发运、损耗与应付；不以总额相等代替逐车可追溯。</p>
    <div v-if="error" class="err">{{ error }}</div>
    <template v-if="r">
      <div class="panel">
        <h2>逐车守恒</h2>
        <table>
          <thead><tr>
            <th>批次</th><th class="num">入库 kg</th><th class="num">在库 kg</th><th class="num">发运 kg</th>
            <th class="num">投炉在产</th><th class="num">加工损耗</th><th class="num">干燥损耗</th><th>重量平衡</th>
            <th class="num">金属入库</th><th class="num">金属在库/发运/损耗</th><th>金属平衡</th>
            <th class="num">当前应付</th><th class="num">已付</th>
          </tr></thead>
          <tbody>
            <tr v-for="t in r.trips" :key="t.trip_id">
              <td class="mono">{{ t.batch_no }}</td>
              <td class="num">{{ kg(t.intake_g) }}</td>
              <td class="num">{{ kg(t.on_hand_g) }}</td>
              <td class="num">{{ kg(t.shipped_g) }}</td>
              <td class="num">{{ kg(t.production_g) }}</td>
              <td class="num">{{ kg(t.process_loss_g) }}</td>
              <td class="num">{{ kg(t.drying_loss_g) }}</td>
              <td>
                <span v-if="t.intake_g === 0" class="muted">—</span>
                <span v-else class="badge" :class="{ danger: !t.weight_balanced }">
                  {{ t.weight_balanced ? '平衡' : `差 ${kg(t.intake_g - t.on_hand_g - t.production_g - t.shipped_g - t.process_loss_g - t.drying_loss_g)}` }}
                </span>
              </td>
              <td class="num">{{ mt(t.intake_metal_mg) }}</td>
              <td class="num">{{ mt(t.on_hand_metal_mg) }} / {{ mt(t.shipped_metal_mg) }} / {{ mt(t.loss_metal_mg) }}</td>
              <td>
                <span v-if="t.intake_metal_mg === 0" class="muted">—</span>
                <span v-else class="badge" :class="{ danger: !t.metal_balanced }">
                  {{ t.metal_balanced ? '平衡' : `差 ${mt(t.intake_metal_mg - t.on_hand_metal_mg - t.shipped_metal_mg - t.loss_metal_mg)}` }}
                </span>
              </td>
              <td class="num">¥{{ yuan(t.payable_cents) }}</td>
              <td class="num">¥{{ yuan(t.paid_cents) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="grid cols-2">
        <div class="panel">
          <h2>按堆位</h2>
          <table>
            <thead><tr><th>堆位</th><th>类型</th><th class="num">重量 kg</th><th class="num">金属 kg</th></tr></thead>
            <tbody>
              <tr v-for="p in r.piles" :key="p.pile_id">
                <td>{{ p.pile_name }}</td><td><span class="badge gray">{{ p.pile_kind }}</span></td>
                <td class="num">{{ kg(p.weight_g) }}</td><td class="num">{{ mt(p.metal_mg) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="panel">
          <h2>全场合计</h2>
          <dl class="kv">
            <dt>入库重量</dt><dd class="num">{{ kg(r.totals.intake_g) }} kg</dd>
            <dt>在库</dt><dd class="num">{{ kg(r.totals.on_hand_g) }} kg</dd>
            <dt>投炉在产</dt><dd class="num">{{ kg(r.totals.production_g) }} kg</dd>
            <dt>发运</dt><dd class="num">{{ kg(r.totals.shipped_g) }} kg</dd>
            <dt>损耗</dt><dd class="num">{{ kg(r.totals.loss_g) }} kg</dd>
            <dt>重量尾差</dt>
            <dd class="num" :style="{color: r.totals.gap_g===0?'var(--accent)':'var(--danger)'}">{{ kg(r.totals.gap_g) }} kg</dd>
            <dt>复检金属增减</dt><dd class="num">{{ mt(r.totals.metal_delta_mg) }} kg</dd>
            <dt>金属尾差</dt>
            <dd class="num" :style="{color: r.totals.gap_metal_mg===0?'var(--accent)':'var(--danger)'}">{{ mt(r.totals.gap_metal_mg) }} kg</dd>
          </dl>
        </div>
      </div>
    </template>
  </div>
</template>
