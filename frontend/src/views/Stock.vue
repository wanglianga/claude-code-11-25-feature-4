<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, getUser, kg } from '../api/client.js'

const user = getUser()
const canYard = computed(() => ['yard', 'admin'].includes(user?.role))
const stock = ref([])
const piles = ref([])
const error = ref('')
const notice = ref('')
const selected = ref(null)
const trace = ref(null)
const timeline = ref([])
const links = ref([])
const busy = ref(false)

const mode = ref('uniform_share')
const srcs = ref([{ lot_id: '', weight_kg: '' }])
const dests = ref([{ pile_id: '', weight_kg: '', grade_code: 'CU-BRIGHT' }])
const losses = ref([])
const refNo = ref('')
const kind = ref('mix')

async function load() {
  try {
    stock.value = await api.get('/api/stock')
    piles.value = await api.get('/api/piles')
  } catch (e) { error.value = e.message }
}
onMounted(load)

async function openLot(lot) {
  error.value = ''
  try {
    selected.value = lot
    trace.value = await api.get(`/api/lots/${lot.id}/trace`)
    timeline.value = await api.get(`/api/lots/${lot.id}/timeline`)
    links.value = trace.value.downstream
  } catch (e) { error.value = e.message }
}

function addSrc() { srcs.value.push({ lot_id: '', weight_kg: '' }) }
function addDest() { dests.value.push({ pile_id: '', weight_kg: '', grade_code: 'CU-BRIGHT' }) }
function addLoss(cls) { losses.value.push({ weight_kg: '', loss_class: cls, note: '' }) }
function removeLoss(i) { losses.value.splice(i, 1) }

async function submit() {
  error.value = ''; notice.value = ''; busy.value = true
  try {
    const body = {
      external_ref: refNo.value || `UI-${Date.now()}`,
      kind: kind.value,
      mode: mode.value,
      occurred_at: new Date().toISOString(),
      note: '页面提交',
      sources: srcs.value.filter(s => s.lot_id && s.weight_kg).map(s => ({
        lot_id: Number(s.lot_id),
        weight_g: Math.round(Number(s.weight_kg) * 1000),
        dest_pile_id: mode.value === 'uniform_share' ? null : (s.dest_pile_id ? Number(s.dest_pile_id) : null),
      })),
      destinations: dests.value.filter(d => d.pile_id && d.weight_kg).map(d => ({
        pile_id: Number(d.pile_id),
        weight_g: Math.round(Number(d.weight_kg) * 1000),
        grade_code: d.grade_code,
      })),
      losses: losses.value.filter(l => l.weight_kg).map(l => ({
        weight_g: Math.round(Number(l.weight_kg) * 1000), loss_class: l.loss_class, note: l.note,
      })),
    }
    const m = await api.post('/api/transfers', body)
    notice.value = `转移单 ${m.external_ref} 已应用（幂等，重放不会二次移动）`
    srcs.value = [{ lot_id: '', weight_kg: '' }]
    dests.value = [{ pile_id: '', weight_kg: '', grade_code: 'CU-BRIGHT' }]
    losses.value = []
    await load()
  } catch (e) { error.value = e.message } finally { busy.value = false }
}

const liveLots = computed(() => {
  const out = []
  for (const s of stock.value) for (const l of s.lots) if (l.weight_g > 0) out.push(l)
  return out
})
</script>

<template>
  <div>
    <h1>料堆与追溯</h1>
    <p class="lead">物料追溯以每次动作后的库存状态为节点；料回到同一物理堆位也形成新的状态节点，不按堆位名判环。</p>
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="notice" class="ok">{{ notice }}</div>

    <div class="grid cols-2">
      <div>
        <div class="panel" v-for="s in stock" :key="s.pile.id">
          <div class="row">
            <h2 style="margin:0">{{ s.pile.name }}</h2>
            <span class="badge gray">{{ s.pile.kind }}</span>
            <span class="spacer"></span>
            <span>{{ kg(s.weight_g) }} kg</span>
          </div>
          <p class="muted" style="font-size:12px;margin:6px 0 10px">{{ s.pile.physical_location }}</p>
          <div class="pill-row" v-if="s.origins.length">
            <span v-for="o in s.origins" :key="o.trip_id" class="badge" style="cursor:default">
              {{ o.batch_no }} · {{ kg(o.weight_g) }}kg · {{ (o.share_ppm/10000).toFixed(1) }}%
            </span>
            <span v-if="s.unknown_share" class="badge warn">来源比例未知</span>
          </div>
          <table v-if="s.lots.length" style="margin-top:8px">
            <thead><tr><th>批次节点</th><th class="num">重量 kg</th><th>状态</th><th></th></tr></thead>
            <tbody>
              <tr v-for="l in s.lots" :key="l.id">
                <td class="mono">{{ l.lot_key }}</td>
                <td class="num">{{ kg(l.weight_g) }}</td>
                <td><span class="badge gray">{{ l.state }}</span></td>
                <td><button class="ghost small" @click="openLot(l)">追溯</button></td>
              </tr>
            </tbody>
          </table>
          <p v-else class="muted" style="font-size:12px">空</p>
        </div>
      </div>

      <div>
        <div class="panel" v-if="selected">
          <h2>追溯：{{ selected.lot_key }}</h2>
          <dl class="kv">
            <dt>当前堆位</dt><dd>{{ selected.pile_name }}</dd>
            <dt>余额</dt><dd>{{ kg(selected.weight_g) }} kg · 金属 {{ (selected.metal_mg/1e6).toFixed(3) }} kg</dd>
          </dl>
          <h3>原车来源</h3>
          <table>
            <thead><tr><th>批次</th><th class="num">重量 kg</th><th class="num">占比</th><th>依据</th><th></th></tr></thead>
            <tbody>
              <tr v-for="o in selected.origins" :key="o.trip_id">
                <td class="mono">{{ o.batch_no || ('#'+o.trip_id) }}</td>
                <td class="num">{{ kg(o.weight_g) }}</td>
                <td class="num">{{ (o.share_ppm/10000).toFixed(2) }}%</td>
                <td><span class="badge gray">{{ o.basis }}</span></td>
                <td><router-link :to="`/trips/${o.trip_id}`">车次</router-link></td>
              </tr>
            </tbody>
          </table>
          <h3>下游去向</h3>
          <table>
            <thead><tr><th>节点</th><th>堆位</th><th class="num">重量 kg</th></tr></thead>
            <tbody>
              <tr v-for="n in trace.downstream" :key="n.lot_id">
                <td class="mono">{{ n.lot_key }}</td><td>{{ n.pile_name }}</td><td class="num">{{ kg(n.weight_g) }}</td>
              </tr>
              <tr v-if="!trace.downstream.length"><td colspan="3" class="muted">无下游</td></tr>
            </tbody>
          </table>
          <h3>动作时间线</h3>
          <table>
            <thead><tr><th>时间</th><th>事件</th><th class="num">重量 kg</th><th>堆位</th></tr></thead>
            <tbody>
              <tr v-for="e in timeline" :key="e.id">
                <td class="muted mono">{{ new Date(e.occurred_at).toLocaleString('zh-CN') }}</td>
                <td><span class="badge gray">{{ e.event }}</span></td>
                <td class="num">{{ kg(e.weight_g) }}</td>
                <td>{{ e.pile_name }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="panel" v-else><p class="muted">点击左侧任一批次节点查看追溯。</p></div>

        <div class="panel" v-if="canYard">
          <h2>新建转移单</h2>
          <div class="grid cols-3">
            <div class="field"><label>单号</label><input v-model="refNo" placeholder="留空自动生成" /></div>
            <div class="field"><label>类型</label>
              <select v-model="kind">
                <option value="mix">合堆</option><option value="split">拆分</option>
                <option value="ship">出库</option><option value="loss">损耗</option>
              </select>
            </div>
            <div class="field"><label>分配方式</label>
              <select v-model="mode">
                <option value="uniform_share">均匀混合(按占比)</option>
                <option value="measured">非均匀分拣(来源实测)</option>
                <option value="unknown">来源比例未知</option>
              </select>
            </div>
          </div>
          <h3>转出</h3>
          <div v-for="(s, i) in srcs" :key="i" class="row" style="margin-bottom:8px">
            <select v-model="s.lot_id" style="flex:2">
              <option value="" disabled>选择批次</option>
              <option v-for="l in liveLots" :key="l.id" :value="l.id">{{ l.lot_key }} · {{ kg(l.weight_g) }}kg</option>
            </select>
            <input v-model="s.weight_kg" type="number" placeholder="重量 kg" style="flex:1" />
            <select v-if="mode !== 'uniform_share'" v-model="s.dest_pile_id" style="flex:1">
              <option value="">去向堆位</option>
              <option v-for="p in piles" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
          </div>
          <button class="subtle" style="font-size:12px;padding:4px 10px" @click="addSrc">+ 转出行</button>

          <h3>转入</h3>
          <div v-for="(dl, i) in dests" :key="i" class="row" style="margin-bottom:8px">
            <select v-model="dl.pile_id" style="flex:2">
              <option value="" disabled>选择堆位</option>
              <option v-for="p in piles" :key="p.id" :value="p.id">{{ p.name }} ({{ p.kind }})</option>
            </select>
            <input v-model="dl.weight_kg" type="number" placeholder="重量 kg" style="flex:1" />
            <input v-model="dl.grade_code" placeholder="等级" style="flex:1" />
          </div>
          <button class="subtle" style="font-size:12px;padding:4px 10px" @click="addDest">+ 转入行</button>

          <h3>明确损耗</h3>
          <div v-for="(l, i) in losses" :key="i" class="row" style="margin-bottom:8px">
            <select v-model="l.loss_class" style="flex:1">
              <option value="drying">干燥(仅减重)</option><option value="process">加工(减重减金属)</option>
            </select>
            <input v-model="l.weight_kg" type="number" placeholder="重量 kg" style="flex:1" />
            <button class="subtle" style="font-size:12px;padding:4px 8px" @click="removeLoss(i)">移除</button>
          </div>
          <div class="row">
            <button class="subtle" style="font-size:12px;padding:4px 10px" @click="addLoss('drying')">+ 干燥损耗</button>
            <button class="subtle" style="font-size:12px;padding:4px 10px" @click="addLoss('process')">+ 加工损耗</button>
          </div>
          <p class="muted" style="font-size:12px;margin:12px 0">
            转出重量 − 明确损耗 = 转入重量必须精确成立，否则先阻断；超库存、隔离料入可用堆、关联成环均会被拒绝。
          </p>
          <p class="warnbox" style="font-size:12px">
            “非均匀分拣(来源实测)”需要在每条转出行补充来源实测明细（origins），页面不采集这两种字段，
            请通过 <code>POST /api/transfers</code> 或 <code>samples/transfers/measured-sort.json</code> 提交；
            页面适合做均匀混合、拆分与出库。
          </p>
          <button :disabled="busy" @click="submit">提交转移单</button>
        </div>
      </div>
    </div>
  </div>
</template>
