<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, getUser, kg, yuan } from '../api/client.js'

const user = getUser()
const canFinance = computed(() => ['finance', 'admin'].includes(user?.role))
const payables = ref([])
const error = ref('')
const notice = ref('')
const openTrip = ref(null)
const versions = ref(null)
const pv = ref(null)

async function load() {
  try { payables.value = await api.get('/api/payables') } catch (e) { error.value = e.message }
}
onMounted(load)

async function expand(p) {
  openTrip.value = p.trip_id
  try {
    versions.value = await api.get(`/api/trips/${p.trip_id}/payable/versions`)
    pv.value = await api.get(`/api/trips/${p.trip_id}/payable/preview`)
  } catch (e) { error.value = e.message }
}

async function approve(p) {
  error.value = ''; notice.value = ''
  try { await api.post(`/api/payables/${p.id}/approve`, { note: '核对差额后批准' }); notice.value = '已批准'; await load(); await expand(p) }
  catch (e) { error.value = e.message }
}
async function pay(p) {
  error.value = ''; notice.value = ''
  try { await api.post(`/api/payables/${p.id}/pay`, {}); notice.value = '已标记付款'; await load(); await expand(p) }
  catch (e) { error.value = e.message }
}

const current = computed(() => payables.value.filter(p => p.is_current))
</script>

<template>
  <div>
    <h1>应付款</h1>
    <p class="lead">从一笔扣款可展开净重、品位与合同计算；调整保留原单，新版本追加。</p>
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="notice" class="ok">{{ notice }}</div>

    <div class="panel">
      <h2>当前有效版本</h2>
      <table>
        <thead><tr>
          <th>批次</th><th>版本</th><th class="num">湿净重 kg</th><th class="num">干重 kg</th>
          <th class="num">扣杂物 kg</th><th class="num">金属 kg</th><th class="num">应付</th><th>状态</th><th></th>
        </tr></thead>
        <tbody>
          <tr v-for="p in current" :key="p.id">
            <td class="mono">{{ p.batch_no }}</td>
            <td>v{{ p.version }}</td>
            <td class="num">{{ kg(p.wet_net_g) }}</td>
            <td class="num">{{ kg(p.dry_g) }}</td>
            <td class="num">{{ kg(p.impurity_g) }}</td>
            <td class="num">{{ (p.metal_mg/1e6).toFixed(3) }}</td>
            <td class="num"><strong>¥{{ yuan(p.amount_cents) }}</strong></td>
            <td><span class="badge" :class="{ warn: p.status==='calculated'||p.status==='held' }">{{ p.status }}</span></td>
            <td>
              <button class="ghost small" @click="expand(p)">展开</button>
              <button v-if="canFinance && (p.status==='calculated'||p.status==='held')" class="subtle"
                style="font-size:12px;padding:4px 8px" @click="approve(p)">批准</button>
              <button v-if="canFinance && p.status==='approved'" class="subtle"
                style="font-size:12px;padding:4px 8px" @click="pay(p)">付款</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="panel" v-if="versions">
      <h2>{{ versions.batch_no }} · 版本历史</h2>
      <div v-if="pv && pv.current" class="row" style="margin-bottom:12px;gap:20px">
        <span>重算预览：净重差 {{ kg(pv.delta_wet_net_g) }}kg</span>
        <span>金属差 {{ (pv.delta_metal_mg/1e6).toFixed(3) }}kg</span>
        <span :style="{ color: pv.delta_amount_cents < 0 ? 'var(--danger)' : 'var(--accent)' }">
          应付差 ¥{{ yuan(pv.delta_amount_cents) }}
        </span>
      </div>
      <table>
        <thead><tr><th>版本</th><th class="num">净重 kg</th><th class="num">金属 kg</th><th class="num">金额</th><th>状态</th><th>调整</th></tr></thead>
        <tbody>
          <tr v-for="v in versions.versions" :key="v.id">
            <td>v{{ v.version }} <span v-if="v.is_current" class="badge">当前</span></td>
            <td class="num">{{ kg(v.wet_net_g) }}</td>
            <td class="num">{{ (v.metal_mg/1e6).toFixed(3) }}</td>
            <td class="num">¥{{ yuan(v.amount_cents) }}</td>
            <td><span class="badge gray">{{ v.status }}</span></td>
            <td style="font-size:12px">
              <div v-for="a in v.adjustments" :key="a.id">
                原 {{ (a.old_metal_mg/1e6).toFixed(3) }} → {{ (a.new_metal_mg/1e6).toFixed(3) }}kg，
                ¥{{ yuan(a.delta_cents) }}
              </div>
              <span v-if="!v.adjustments.length" class="muted">—</span>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="muted" style="font-size:12px;margin-top:10px">累计已付：¥{{ yuan(versions.total_paid_cents) }}</p>
    </div>
  </div>
</template>
