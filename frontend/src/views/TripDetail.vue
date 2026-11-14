<script setup>
import { ref, onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { api, getUser, kg, yuan, ppmPct } from '../api/client.js'

const route = useRoute()
const id = Number(route.params.id)
const user = getUser()
const d = ref(null)
const piles = ref([])
const error = ref('')
const notice = ref('')
const busy = ref(false)

const wForm = ref({ kind: 'gross', weight_kg: '', ticket_no: '', scale_device: 'SCALE-N-01' })
const adoptForm = ref({ gross_weighing_id: '', tare_weighing_id: '', basis_note: '' })
const sampleForm = ref({ sample_no: '', position: '', seal_no: '', note: '' })
const assayForm = ref({ sample_id: '', moisture_pct: '', impurity_pct: '', impurity_basis: 'dry', metal_pct: '', metal_basis: 'dry' })
const gradeForm = ref({ grade_code: 'CU-BRIGHT', isolated: false, note: '' })
const intakeForm = ref({ pile_id: '' })
const preview = ref(null)

const role = computed(() => user?.role || '')
const can = (...roles) => roles.includes(role.value) || role.value === 'admin'

const grosses = computed(() => (d.value?.weighings || []).filter(w => w.kind === 'gross'))
const tares = computed(() => (d.value?.weighings || []).filter(w => w.kind === 'tare'))
const adoptedAssay = computed(() => (d.value?.assays || []).find(a => a.quality_status === 'adopted'))

async function load() {
  error.value = ''
  try {
    d.value = await api.get(`/api/trips/${id}`)
    piles.value = await api.get('/api/piles')
    preview.value = await api.get(`/api/trips/${id}/payable/preview`)
  } catch (e) { error.value = e.message }
}
onMounted(load)

async function act(fn) {
  error.value = ''; notice.value = ''; busy.value = true
  try { const msg = await fn(); notice.value = msg || ''; await load() }
  catch (e) { error.value = e.message }
  finally { busy.value = false }
}

const addWeighing = () => act(async () => {
  if (!wForm.value.weight_kg) throw new Error('请输入重量(kg)')
  const grams = Math.round(Number(wForm.value.weight_kg) * 1000)
  await api.post(`/api/trips/${id}/weighings`, {
    kind: wForm.value.kind, weight_g: grams, ticket_no: wForm.value.ticket_no,
    scale_device: wForm.value.scale_device, weighed_at: new Date().toISOString()
  })
  wForm.value = { kind: 'gross', weight_kg: '', ticket_no: '', scale_device: 'SCALE-N-01' }
  return '称次已登记'
})

const adopt = () => act(async () => {
  await api.post(`/api/trips/${id}/adopt`, {
    gross_weighing_id: Number(adoptForm.value.gross_weighing_id),
    tare_weighing_id: Number(adoptForm.value.tare_weighing_id),
    basis_note: adoptForm.value.basis_note
  })
  return '已采用称次（原采用记录保留）'
})

const addSample = () => act(async () => {
  await api.post(`/api/trips/${id}/samples`, sampleForm.value)
  sampleForm.value = { sample_no: '', position: '', seal_no: '', note: '' }
  return '封样已绑定车次与取样位置'
})

const addAssay = () => act(async () => {
  const body = {
    sample_id: Number(assayForm.value.sample_id),
    moisture_ppm: Math.round(Number(assayForm.value.moisture_pct) * 10000),
    impurity_ppm: assayForm.value.impurity_pct === '' ? null : Math.round(Number(assayForm.value.impurity_pct) * 10000),
    impurity_basis: assayForm.value.impurity_basis,
    metal_ppm: Math.round(Number(assayForm.value.metal_pct) * 10000),
    metal_basis: assayForm.value.metal_basis,
  }
  // path expects sample id
  const sid = body.sample_id
  const { sample_id, ...payload } = body
  await api.post(`/api/samples/${sid}/assays`, payload)
  assayForm.value.moisture_pct = ''; assayForm.value.metal_pct = ''; assayForm.value.impurity_pct = ''
  return '化验结果已提交，等待质量人员确认采用'
})

const adoptAssay = (a) => act(async () => {
  await api.post(`/api/assays/${a.id}/adopt`, { note: '质量确认采用' })
  return '已确认采用该化验结果'
})
const rejectAssay = (a) => act(async () => {
  await api.post(`/api/assays/${a.id}/reject`, { note: '不予采用' })
  return '已拒绝该化验结果'
})

const approveGrade = () => act(async () => {
  await api.post(`/api/trips/${id}/grade`, gradeForm.value)
  return '等级已批准'
})

const intake = () => act(async () => {
  await api.post(`/api/trips/${id}/intake`, { pile_id: Number(intakeForm.value.pile_id) })
  return '来料已入库'
})

const computePayable = () => act(async () => {
  await api.post(`/api/trips/${id}/payable`, { note: '按当前采用称次与化验重算' })
  return '应付新版本已生成（旧版本保留）'
})
const approvePayable = (p) => act(async () => {
  await api.post(`/api/payables/${p.id}/approve`, { note: '核对新旧差额后批准' })
  return '应付款已批准'
})
const payPayable = (p) => act(async () => {
  await api.post(`/api/payables/${p.id}/pay`, {})
  return '已标记付款'
})
const reassay = () => act(async () => {
  const r = await api.post(`/api/trips/${id}/reassay`, {})
  return `复检已沿来源关系调整 ${r.affected_lots.length} 个存量节点，应付差额 ¥${yuan(r.payable?.delta_amount_cents || 0)}`
})

const usablePiles = computed(() => piles.value.filter(p => ['usable', 'receiving', 'isolated'].includes(p.kind)))
</script>

<template>
  <div v-if="d">
    <div class="row" style="margin-bottom:6px">
      <h1 style="margin:0">{{ d.trip.batch_no }}</h1>
      <span class="badge gray">{{ d.trip.contract_code }}</span>
      <span class="spacer"></span>
      <router-link to="/trips" class="muted">← 返回车次列表</router-link>
    </div>
    <p class="lead">{{ d.trip.supplier_name }} · {{ d.trip.plate_no }} · {{ d.trip.driver }} ·
      {{ new Date(d.trip.arrival_at).toLocaleString('zh-CN') }}</p>
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="notice" class="ok">{{ notice }}</div>

    <div class="grid cols-2">
      <div class="panel">
        <h2>称重</h2>
        <table>
          <thead><tr><th>类型</th><th>第几次</th><th class="num">重量 (kg)</th><th>磅单</th><th>设备</th></tr></thead>
          <tbody>
            <tr v-for="w in d.weighings" :key="w.id">
              <td>{{ w.kind === 'gross' ? '毛重' : '皮重' }}</td>
              <td>#{{ w.seq }}</td>
              <td class="num">{{ kg(w.weight_g) }}</td>
              <td class="mono">{{ w.ticket_no }}</td>
              <td class="muted">{{ w.scale_device }}</td>
            </tr>
            <tr v-if="!d.weighings.length"><td colspan="5" class="muted">尚无称次</td></tr>
          </tbody>
        </table>

        <div v-if="d.adoption" class="adoption-box">
          <div class="row">
            <span class="badge">当前采用</span>
            <span class="muted">毛重 {{ kg(d.adoption.gross_weight_g) }} kg − 皮重 {{ kg(d.adoption.tare_weight_g) }} kg</span>
            <span class="spacer"></span>
            <strong>净重 {{ kg(d.adoption.net_g) }} kg</strong>
          </div>
          <p class="muted" style="font-size:12px;margin:8px 0 0">依据：{{ d.adoption.basis_note }}</p>
        </div>
        <div v-else class="warnbox">尚无采用称次</div>

        <div v-if="can('yard','purchasing')" style="margin-top:14px">
          <h3>登记称次</h3>
          <div class="grid cols-4">
            <div class="field"><label>类型</label>
              <select v-model="wForm.kind"><option value="gross">毛重</option><option value="tare">皮重</option></select>
            </div>
            <div class="field"><label>重量 (kg)</label><input v-model="wForm.weight_kg" type="number" step="0.001" /></div>
            <div class="field"><label>磅单号</label><input v-model="wForm.ticket_no" /></div>
            <div class="field"><label>设备</label><input v-model="wForm.scale_device" /></div>
          </div>
          <button :disabled="busy" @click="addWeighing">登记</button>

          <h3>采用毛重 / 皮重</h3>
          <div class="grid cols-3">
            <div class="field"><label>毛重称次</label>
              <select v-model="adoptForm.gross_weighing_id">
                <option value="" disabled>选择</option>
                <option v-for="w in grosses" :key="w.id" :value="w.id">#{{ w.seq }} · {{ kg(w.weight_g) }}kg</option>
              </select>
            </div>
            <div class="field"><label>皮重称次</label>
              <select v-model="adoptForm.tare_weighing_id">
                <option value="" disabled>选择</option>
                <option v-for="w in tares" :key="w.id" :value="w.id">#{{ w.seq }} · {{ kg(w.weight_g) }}kg</option>
              </select>
            </div>
            <div class="field"><label>采用依据(必填)</label><input v-model="adoptForm.basis_note" placeholder="复称原因/依据" /></div>
          </div>
          <button :disabled="busy" @click="adopt">采用</button>
        </div>
      </div>

      <div class="panel">
        <h2>封样与化验</h2>
        <h3>封样（必须绑定取样位置）</h3>
        <table>
          <thead><tr><th>样号</th><th>取样位置</th><th>封签</th></tr></thead>
          <tbody>
            <tr v-for="s in d.samples" :key="s.id">
              <td class="mono">{{ s.sample_no }}</td><td>{{ s.position }}</td><td class="mono">{{ s.seal_no }}</td>
            </tr>
            <tr v-if="!d.samples.length"><td colspan="3" class="muted">尚无封样</td></tr>
          </tbody>
        </table>
        <div v-if="can('lab','yard')" style="margin-top:10px">
          <div class="grid cols-3">
            <div class="field"><label>样号</label><input v-model="sampleForm.sample_no" /></div>
            <div class="field"><label>取样位置</label><input v-model="sampleForm.position" placeholder="如 车厢前部" /></div>
            <div class="field"><label>封签号</label><input v-model="sampleForm.seal_no" /></div>
          </div>
          <button class="subtle" :disabled="busy" @click="addSample">新增封样</button>
        </div>

        <h3>化验结果</h3>
        <table>
          <thead><tr><th>版本</th><th class="num">水分</th><th class="num">杂物</th><th class="num">金属</th><th>状态</th><th></th></tr></thead>
          <tbody>
            <tr v-for="a in d.assays" :key="a.id">
              <td>v{{ a.version }}</td>
              <td class="num">{{ ppmPct(a.moisture_ppm) }}</td>
              <td class="num">{{ a.impurity_ppm == null ? '—' : ppmPct(a.impurity_ppm) }}
                <span class="muted">({{ a.impurity_basis }})</span></td>
              <td class="num">{{ ppmPct(a.metal_ppm) }} <span class="muted">({{ a.metal_basis }})</span></td>
              <td>
                <span class="badge" :class="{
                  gray: a.quality_status === 'pending',
                  warn: a.quality_status === 'superseded' || a.quality_status === 'rejected'
                }">{{ a.quality_status }}</span>
              </td>
              <td>
                <button v-if="can('quality') && (a.quality_status === 'pending' || a.quality_status === 'adopted')"
                  class="ghost small" :disabled="busy" @click="adoptAssay(a)">采用</button>
                <button v-if="can('quality') && a.quality_status === 'pending'"
                  class="subtle" style="font-size:12px;padding:4px 8px" :disabled="busy" @click="rejectAssay(a)">拒绝</button>
              </td>
            </tr>
            <tr v-if="!d.assays.length"><td colspan="6" class="muted">尚无化验</td></tr>
          </tbody>
        </table>

        <div v-if="can('lab') && d.samples.length" style="margin-top:10px">
          <div class="grid cols-3">
            <div class="field"><label>封样</label>
              <select v-model="assayForm.sample_id">
                <option value="" disabled>选择</option>
                <option v-for="s in d.samples" :key="s.id" :value="s.id">{{ s.sample_no }} · {{ s.position }}</option>
              </select>
            </div>
            <div class="field"><label>水分 %</label><input v-model="assayForm.moisture_pct" type="number" step="0.001" /></div>
            <div class="field"><label>非金属杂物 %</label><input v-model="assayForm.impurity_pct" type="number" step="0.001" /></div>
            <div class="field"><label>杂物基准</label>
              <select v-model="assayForm.impurity_basis"><option value="dry">干基</option><option value="wet">湿基</option></select>
            </div>
            <div class="field"><label>金属含量 %</label><input v-model="assayForm.metal_pct" type="number" step="0.001" /></div>
            <div class="field"><label>金属基准</label>
              <select v-model="assayForm.metal_basis"><option value="dry">干基</option><option value="wet">湿基</option></select>
            </div>
          </div>
          <button class="subtle" :disabled="busy" @click="addAssay">提交化验</button>
        </div>
      </div>
    </div>

    <div class="grid cols-2">
      <div class="panel">
        <h2>质量等级</h2>
        <div v-if="d.grade">
          <dl class="kv">
            <dt>等级</dt><dd>{{ d.grade.grade_code }}</dd>
            <dt>系数</dt><dd>{{ (d.grade.coef_ppm / 10000).toFixed(3) }}</dd>
            <dt>状态</dt><dd><span class="badge">{{ d.grade.status }}</span>
              <span v-if="d.grade.isolated" class="badge danger" style="margin-left:6px">隔离料</span></dd>
            <dt>备注</dt><dd class="muted">{{ d.grade.note }}</dd>
          </dl>
        </div>
        <div v-else class="warnbox">尚无批准等级</div>
        <div v-if="can('quality')" style="margin-top:12px">
          <div class="grid cols-3">
            <div class="field"><label>等级</label>
              <select v-model="gradeForm.grade_code">
                <option value="CU-BRIGHT">CU-BRIGHT</option>
                <option value="CU-MIX">CU-MIX</option>
                <option value="CU-DIRTY">CU-DIRTY</option>
              </select>
            </div>
            <div class="field"><label>隔离</label>
              <select v-model="gradeForm.isolated"><option :value="false">否</option><option :value="true">是</option></select>
            </div>
            <div class="field"><label>备注</label><input v-model="gradeForm.note" /></div>
          </div>
          <button :disabled="busy" @click="approveGrade">批准等级</button>
        </div>
      </div>

      <div class="panel">
        <h2>入库</h2>
        <div v-if="d.lots.length">
          <table>
            <thead><tr><th>批次</th><th>堆位</th><th class="num">重量 kg</th><th class="num">金属 kg</th></tr></thead>
            <tbody>
              <tr v-for="l in d.lots" :key="l.id">
                <td class="mono">{{ l.lot_key }}</td><td>{{ l.pile_name }}</td>
                <td class="num">{{ kg(l.weight_g) }}</td><td class="num">{{ (l.metal_mg / 1e6).toFixed(3) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else>
          <p class="muted">尚未入库。需要：采用称次 + 质量采用化验 + 已批准等级。</p>
          <div v-if="can('yard','purchasing')">
            <div class="field"><label>入库堆位</label>
              <select v-model="intakeForm.pile_id">
                <option value="" disabled>选择</option>
                <option v-for="p in usablePiles" :key="p.id" :value="p.id">{{ p.name }} ({{ p.kind }})</option>
              </select>
            </div>
            <button :disabled="busy" @click="intake">入库</button>
          </div>
        </div>
      </div>
    </div>

    <div class="panel">
      <h2>应付与付款</h2>
      <div v-if="preview">
        <div v-if="!preview.calculation.can_pay" class="warnbox">
          暂停付款：{{ preview.calculation.blocked_reason || '参数不全' }}
          <ul class="plain" style="margin-top:6px">
            <li v-for="w in preview.calculation.warnings" :key="w">{{ w }}</li>
          </ul>
        </div>
        <template v-else>
          <div class="flowsteps" style="margin-bottom:12px">
            <div class="flowstep">湿净重<b>{{ kg(preview.calculation.result.wet_net_g) }} kg</b></div>
            <span class="arrow">−水</span>
            <div class="flowstep">扣除水分<b>{{ kg(preview.calculation.result.moisture_g) }} kg</b></div>
            <span class="arrow">=</span>
            <div class="flowstep">干重<b>{{ kg(preview.calculation.result.dry_g) }} kg</b></div>
            <span class="arrow">−杂物</span>
            <div class="flowstep">非金属杂物<b>{{ kg(preview.calculation.result.impurity_g) }} kg</b></div>
            <span class="arrow">=</span>
            <div class="flowstep">计价料<b>{{ kg(preview.calculation.result.priced_g) }} kg</b></div>
            <span class="arrow">×金属比例</span>
            <div class="flowstep">计价金属<b>{{ (preview.calculation.result.metal_mg / 1e6).toFixed(3) }} kg</b></div>
            <span class="arrow">×单价×系数</span>
            <div class="flowstep">应付<b>¥{{ yuan(preview.calculation.result.amount_cents) }}</b></div>
          </div>
          <p class="muted" style="font-size:12px">
            公式 {{ preview.calculation.formula_code }}：湿净重先扣水得干重，干重再扣已单列非金属杂物，
            剩余材料乘干基金属比例，再乘约定单价与等级系数。
          </p>
        </template>

        <div v-if="preview.current" class="row" style="gap:20px;margin:12px 0">
          <div>当前版本 v{{ preview.current.version }} · ¥{{ yuan(preview.current.amount_cents) }}</div>
          <div :style="{ color: preview.delta_amount_cents < 0 ? 'var(--danger)' : 'var(--accent)' }">
            新版本差额：净重 {{ kg(preview.delta_wet_net_g) }}kg · 金属 {{ (preview.delta_metal_mg / 1e6).toFixed(3) }}kg ·
            ¥{{ yuan(preview.delta_amount_cents) }}
          </div>
        </div>
        <div class="row" v-if="can('finance','purchasing')">
          <button :disabled="busy || !preview.calculation.can_pay" @click="computePayable">
            {{ preview.current ? '生成新版本' : '计算应付' }}
          </button>
          <button v-if="can('quality','finance') && preview.current" class="subtle" :disabled="busy" @click="reassay">
            复检并沿来源调整
          </button>
        </div>
      </div>

      <div v-if="d.payable" style="margin-top:16px">
        <h3>当前版本</h3>
        <dl class="kv">
          <dt>金额</dt><dd class="num"><strong>¥{{ yuan(d.payable.amount_cents) }}</strong></dd>
          <dt>状态</dt><dd><span class="badge" :class="{ warn: d.payable.status !== 'approved' && d.payable.status !== 'paid' }">{{ d.payable.status }}</span></dd>
          <dt>调整记录</dt>
          <dd>
            <div v-for="a in d.payable.adjustments" :key="a.id" style="margin-bottom:4px">
              金属 {{ (a.old_metal_mg / 1e6).toFixed(3) }}→{{ (a.new_metal_mg / 1e6).toFixed(3) }}kg，
              差额 ¥{{ yuan(a.delta_cents) }} · {{ a.reason }}
            </div>
            <span v-if="!d.payable.adjustments.length" class="muted">—</span>
          </dd>
        </dl>
        <div class="row" v-if="can('finance')">
          <button v-if="d.payable.status === 'calculated' || d.payable.status === 'held'" :disabled="busy"
            @click="approvePayable(d.payable)">批准</button>
          <button v-if="d.payable.status === 'approved'" :disabled="busy" @click="payPayable(d.payable)">标记付款</button>
        </div>
      </div>
    </div>
  </div>
  <div v-else>
    <div v-if="error" class="err">{{ error }}</div>
    <p class="muted">加载中…</p>
  </div>
</template>

<style scoped>
.adoption-box { margin-top: 14px; background: #faf8f4; border: 1px solid var(--line); border-radius: 8px; padding: 12px 14px; }
</style>
