<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, getUser, yuan, ppmPct } from '../api/client.js'

const user = getUser()
const canQuality = computed(() => ['quality', 'admin'].includes(user?.role))
const canLab = computed(() => ['lab', 'quality', 'yard', 'admin'].includes(user?.role))
const canFinance = computed(() => ['finance', 'admin'].includes(user?.role))

const cases = ref([])
const open = ref(null)
const trial = ref(null)
const units = ref([])
const error = ref('')
const notice = ref('')
const busy = ref(false)

const reportForm = ref({
  party: 'supplier', report_no: '', lab_name: '', sample_no: '', seal_no: '', position: '',
  declared_unit: '%', moisture_pct: '', impurity_pct: '', impurity_basis: 'dry', metal_pct: '', metal_basis: 'dry',
  quote_yuan: ''
})
const drawForm = ref({ unit_id: '', weight_g: '', blind_code: '', lab_name: '' })
const retForm = ref({ blind_code: '', lab_name: '', sample_no: '', seal_no: '', position: '', declared_unit: '%',
  moisture_pct: '', impurity_pct: '', impurity_basis: 'dry', metal_pct: '', metal_basis: 'dry' })
const adjForm = ref({ adopted: 'plant', reason: '', acknowledge_incomparable: false })

async function load() {
  try { cases.value = await api.get('/api/review-cases') } catch (e) { error.value = e.message }
}
onMounted(load)

async function act(fn) {
  error.value = ''; notice.value = ''; busy.value = true
  try { const msg = await fn(); notice.value = msg || ''; await load(); if (open.value) await expand(open.value) }
  catch (e) { error.value = e.message }
  finally { busy.value = false }
}

async function expand(c) {
  error.value = ''
  try {
    open.value = c
    const d = await api.get(`/api/review-cases/${c.id}`)
    open.value = d
    trial.value = await api.get(`/api/review-cases/${c.id}/trial`)
    units.value = await api.get(`/api/trips/${c.trip_id}/sample-units`)
    adjForm.value.adopted = d.comparability?.comparable ? 'plant' : 'external'
  } catch (e) { error.value = e.message }
}

const pct = (v) => Math.round(Number(v) * 10000)

const addReport = () => act(async () => {
  const f = reportForm.value
  await api.post(`/api/review-cases/${open.value.id}/party-reports`, {
    party: f.party, report_no: f.report_no, lab_name: f.lab_name, sample_no: f.sample_no,
    seal_no: f.seal_no, position: f.position, declared_unit: f.declared_unit,
    moisture_ppm: pct(f.moisture_pct),
    impurity_ppm: f.impurity_pct === '' ? null : pct(f.impurity_pct), impurity_basis: f.impurity_basis,
    metal_ppm: pct(f.metal_pct), metal_basis: f.metal_basis,
    quote_cents_per_t: f.quote_yuan === '' ? 0 : Math.round(Number(f.quote_yuan) * 100)
  })
  reportForm.value.report_no = ''
  return '报告已登记（证据版本 +1）'
})

const draw = () => act(async () => {
  if (!drawForm.value.unit_id) throw new Error('请选择留样单元')
  if (!drawForm.value.weight_g) throw new Error('请填写领取样量')
  const r = await api.post(`/api/review-cases/${open.value.id}/draws`, {
    unit_id: Number(drawForm.value.unit_id), weight_g: Number(drawForm.value.weight_g),
    blind_code: drawForm.value.blind_code, lab_name: drawForm.value.lab_name
  })
  return `已领取 ${r.weight_g} g，盲编号 ${r.blind_code}，剩余 ${r.remaining_g} g`
})

const ret = () => act(async () => {
  const f = retForm.value
  await api.post(`/api/review-cases/${open.value.id}/external-return`, {
    blind_code: f.blind_code, lab_name: f.lab_name, sample_no: f.sample_no, seal_no: f.seal_no,
    position: f.position, declared_unit: f.declared_unit,
    moisture_ppm: pct(f.moisture_pct),
    impurity_ppm: f.impurity_pct === '' ? null : pct(f.impurity_pct), impurity_basis: f.impurity_basis,
    metal_ppm: pct(f.metal_pct), metal_basis: f.metal_basis
  })
  return '外送结果已按盲编号登记（揭示前数值保密）'
})

const reveal = (e) => act(async () => {
  await api.post(`/api/review-cases/${open.value.id}/reveal`, { external_report_id: e.id, note: '封签一致，正式揭示' })
  return '外送结果已揭示'
})

const adjudicate = () => act(async () => {
  if (!adjForm.value.reason) throw new Error('请填写裁定理由')
  const body = {
    expected_evidence_version: open.value.evidence_version,
    adopted: adjForm.value.adopted,
    reason: adjForm.value.reason,
    acknowledge_incomparable: adjForm.value.acknowledge_incomparable
  }
  if (adjForm.value.adopted === 'external') body.external_report_id = trial.value.candidates.find(c => c.source === 'external')?.external_report_id
  if (adjForm.value.adopted === 'supplier') body.party_report_id = trial.value.candidates.find(c => c.source === 'supplier')?.party_report_id
  if (adjForm.value.adopted === 'plant') body.party_report_id = trial.value.candidates.find(c => c.source === 'plant')?.party_report_id
  await api.post(`/api/review-cases/${open.value.id}/adjudicate`, body)
  return '裁定已生效，付款按已付/未付分别结算'
})

const statusBadge = (s) => ({
  open: 'gray', comparing: 'info', pending_evidence: 'warn', adjudicated: 'info', settled: 'gray'
}[s] || 'gray')
</script>

<template>
  <div>
    <h1>封样复核与差异裁定</h1>
    <p class="lead">
      双方围绕同一车材料判断哪份结果可用于结算。先核查单位、基准与取样代表性；可比才按约定误差比对，
      不可比只说明差异来源，绝不自动择高、择低或平均。
    </p>
    <div v-if="error" class="err">{{ error }}</div>
    <div v-if="notice" class="ok">{{ notice }}</div>

    <div class="panel">
      <h2>复核案件</h2>
      <table>
        <thead><tr><th>案件</th><th>批次</th><th>供应商</th><th>状态</th><th>证据版本</th><th>冻结</th><th></th></tr></thead>
        <tbody>
          <tr v-for="c in cases" :key="c.id">
            <td class="mono">{{ c.case_no }}</td>
            <td class="mono">{{ c.batch_no }}</td>
            <td>{{ c.supplier_name }}</td>
            <td><span class="badge" :class="statusBadge(c.status)">{{ c.status }}</span></td>
            <td>v{{ c.evidence_version }}</td>
            <td>
              <span v-if="c.frozen" class="badge danger">仅本车冻结</span>
              <span v-else class="muted">—</span>
              <span v-if="c.open_rereview" class="badge warn" style="margin-left:6px">待重新复核</span>
            </td>
            <td><button class="ghost small" @click="expand(c)">展开</button></td>
          </tr>
          <tr v-if="!cases.length"><td colspan="7" class="muted">暂无复核案件</td></tr>
        </tbody>
      </table>
    </div>

    <template v-if="open">
      <div class="panel">
        <div class="row">
          <h2 style="margin:0">{{ open.case_no }}</h2>
          <span class="badge gray">{{ open.batch_no }}</span>
          <span class="badge" :class="statusBadge(open.status)">{{ open.status }}</span>
          <span class="spacer"></span>
          <span class="muted">证据版本 v{{ open.evidence_version }}</span>
        </div>
        <p class="muted" style="margin:8px 0 0">{{ open.note }}</p>
        <div v-if="open.freeze" class="warnbox" style="margin-top:10px">
          仅冻结本车付款：{{ open.freeze.reason }}
        </div>
        <div v-if="open.open_rereview" class="warnbox" style="margin-top:10px">
          裁定后新增报告，不能覆盖旧决定：{{ open.open_rereview.reason }}
        </div>
      </div>

      <div class="panel" v-if="open.comparability">
        <h2>可比性核查</h2>
        <div class="row" style="gap:18px;margin-bottom:10px">
          <span>单位 <span class="badge" :class="open.comparability.unit_ok ? '' : 'danger'">{{ open.comparability.unit_ok ? '一致/可换算' : '未登记' }}</span></span>
          <span>基准 <span class="badge" :class="open.comparability.basis_ok ? '' : 'danger'">{{ open.comparability.basis_ok ? '一致' : '干湿基不可比' }}</span></span>
          <span>取样代表性 <span class="badge" :class="open.comparability.sampling_ok ? '' : 'warn'">{{ open.comparability.sampling_ok ? '一致' : '不同位置' }}</span></span>
          <span>结论 <span class="badge" :class="open.comparability.comparable ? 'info' : 'danger'">{{ open.comparability.comparable ? (open.comparability.within_tolerance ? '可比且在容差内' : '可比但超容差') : '不可比' }}</span></span>
        </div>
        <p class="muted" style="font-size:12px">约定误差范围：{{ open.comparability.agreed_range }}</p>
        <ul class="plain">
          <li v-for="s in open.comparability.difference_sources" :key="s">{{ s }}</li>
        </ul>
      </div>

      <div class="grid cols-2">
        <div class="panel">
          <h2>供应商 / 厂内报告</h2>
          <table>
            <thead><tr><th>方</th><th>报告号</th><th class="num">水分</th><th class="num">杂物</th><th class="num">金属</th><th>基准</th></tr></thead>
            <tbody>
              <tr v-for="r in open.party_reports" :key="r.id">
                <td>{{ r.party === 'supplier' ? '供应商' : '厂内' }}</td>
                <td class="mono">{{ r.report_no }}</td>
                <td class="num">{{ ppmPct(r.moisture_ppm) }}</td>
                <td class="num">{{ r.impurity_ppm == null ? '—' : ppmPct(r.impurity_ppm) }}</td>
                <td class="num">{{ ppmPct(r.metal_ppm) }}</td>
                <td class="muted">{{ r.metal_basis }}<span v-if="r.quote_cents_per_t"> · 报价 ¥{{ yuan(r.quote_cents_per_t) }}/t</span></td>
              </tr>
              <tr v-if="!open.party_reports.length"><td colspan="6" class="muted">尚无报告</td></tr>
            </tbody>
          </table>

          <div v-if="canLab" style="margin-top:12px">
            <h3>登记报告</h3>
            <div class="grid cols-3">
              <div class="field"><label>方</label>
                <select v-model="reportForm.party"><option value="supplier">供应商</option><option value="plant">厂内</option></select>
              </div>
              <div class="field"><label>报告号</label><input v-model="reportForm.report_no" /></div>
              <div class="field"><label>化验室</label><input v-model="reportForm.lab_name" /></div>
              <div class="field"><label>样号</label><input v-model="reportForm.sample_no" /></div>
              <div class="field"><label>封签</label><input v-model="reportForm.seal_no" /></div>
              <div class="field"><label>取样位置</label><input v-model="reportForm.position" /></div>
              <div class="field"><label>水分 %</label><input v-model="reportForm.moisture_pct" type="number" step="0.001" /></div>
              <div class="field"><label>杂物 %</label><input v-model="reportForm.impurity_pct" type="number" step="0.001" /></div>
              <div class="field"><label>金属 %</label><input v-model="reportForm.metal_pct" type="number" step="0.001" /></div>
              <div class="field"><label>杂物基准</label>
                <select v-model="reportForm.impurity_basis"><option value="dry">干基</option><option value="wet">湿基</option></select>
              </div>
              <div class="field"><label>金属基准</label>
                <select v-model="reportForm.metal_basis"><option value="dry">干基</option><option value="wet">湿基</option></select>
              </div>
              <div class="field"><label>供应商报价 ¥/t（仅质量/财务可见）</label><input v-model="reportForm.quote_yuan" type="number" /></div>
            </div>
            <button class="subtle" :disabled="busy" @click="addReport">登记报告</button>
          </div>
        </div>

        <div class="panel">
          <h2>留样与外送</h2>
          <table>
            <thead><tr><th>留样</th><th>封签</th><th class="num">初始 g</th><th class="num">剩余 g</th><th>状态</th></tr></thead>
            <tbody>
              <tr v-for="u in units" :key="u.id">
                <td class="mono">{{ u.unit_no }}<span v-if="u.parent_unit_id" class="muted"> (分样)</span></td>
                <td class="mono">{{ u.seal_no }}</td>
                <td class="num">{{ u.initial_g }}</td>
                <td class="num">{{ u.remaining_g }}</td>
                <td>
                  <span class="badge" :class="{ warn: u.status === 'drawn', danger: u.status === 'broken' }">{{ u.status }}</span>
                  <span v-if="u.blind_code" class="mono muted" style="margin-left:6px">{{ u.blind_code }}</span>
                </td>
              </tr>
            </tbody>
          </table>

          <h3>外送盲样</h3>
          <table>
            <thead><tr><th>盲编号</th><th>状态</th><th class="num">金属</th><th class="num">水分</th><th></th></tr></thead>
            <tbody>
              <tr v-for="e in open.external_reports" :key="e.id">
                <td class="mono">{{ e.blind_code }}</td>
                <td><span class="badge" :class="{ warn: e.status === 'pending_evidence' }">{{ e.status }}</span></td>
                <td class="num">{{ e.masked ? '（揭示前保密）' : ppmPct(e.metal_ppm) }}</td>
                <td class="num">{{ e.masked ? '—' : ppmPct(e.moisture_ppm) }}</td>
                <td>
                  <button v-if="canQuality && e.status === 'returned'" class="ghost small" :disabled="busy"
                    @click="reveal(e)">揭示</button>
                </td>
              </tr>
              <tr v-if="!open.external_reports.length"><td colspan="5" class="muted">尚未外送</td></tr>
            </tbody>
          </table>

          <div v-if="canLab" style="margin-top:12px">
            <h3>领取留样</h3>
            <div class="grid cols-2">
              <div class="field"><label>留样单元</label>
                <select v-model="drawForm.unit_id">
                  <option value="" disabled>选择</option>
                  <option v-for="u in units" :key="u.id" :value="u.id"
                    :disabled="u.status !== 'sealed'">{{ u.unit_no }} · {{ u.remaining_g }}g · {{ u.status }}</option>
                </select>
              </div>
              <div class="field"><label>领取样量 (g)</label><input v-model="drawForm.weight_g" type="number" /></div>
              <div class="field"><label>盲编号（留空自动生成）</label><input v-model="drawForm.blind_code" /></div>
              <div class="field"><label>实验室</label><input v-model="drawForm.lab_name" /></div>
            </div>
            <button class="subtle" :disabled="busy" @click="draw">领取外送</button>

            <h3>外送结果返回（盲编号关联）</h3>
            <div class="grid cols-3">
              <div class="field"><label>盲编号</label><input v-model="retForm.blind_code" /></div>
              <div class="field"><label>实验室</label><input v-model="retForm.lab_name" /></div>
              <div class="field"><label>样号</label><input v-model="retForm.sample_no" /></div>
              <div class="field"><label>封签</label><input v-model="retForm.seal_no" /></div>
              <div class="field"><label>取样位置</label><input v-model="retForm.position" /></div>
              <div class="field"><label>水分 %</label><input v-model="retForm.moisture_pct" type="number" step="0.001" /></div>
              <div class="field"><label>杂物 %</label><input v-model="retForm.impurity_pct" type="number" step="0.001" /></div>
              <div class="field"><label>金属 %</label><input v-model="retForm.metal_pct" type="number" step="0.001" /></div>
              <div class="field"><label>金属基准</label>
                <select v-model="retForm.metal_basis"><option value="dry">干基</option><option value="wet">湿基</option></select>
              </div>
            </div>
            <button class="subtle" :disabled="busy" @click="ret">登记返回</button>
          </div>
        </div>
      </div>

      <div class="panel" v-if="trial">
        <h2>试算：受影响车款与混堆去向</h2>
        <p class="muted" style="font-size:12px">{{ trial.note }}</p>
        <table>
          <thead><tr><th>候选结果</th><th class="num">金属</th><th class="num">应付</th><th class="num">差额</th><th>混堆金属变化</th></tr></thead>
          <tbody>
            <tr v-for="c in trial.candidates" :key="c.source">
              <td>{{ c.label }}<span v-if="c.blocked" class="badge warn" style="margin-left:6px">不可计算</span></td>
              <td class="num">{{ ppmPct(c.metal_ppm) }}</td>
              <td class="num">{{ c.blocked ? '—' : '¥' + yuan(c.amount_cents) }}</td>
              <td class="num" :style="{ color: c.delta_cents < 0 ? 'var(--danger)' : 'var(--accent)' }">
                {{ c.blocked ? '—' : '¥' + yuan(c.delta_cents) }}
              </td>
              <td>
                <div v-for="l in c.lot_impacts" :key="l.lot_id" style="font-size:12px">
                  {{ l.pile_name }}（{{ l.lot_key }}）：{{ (l.old_metal_mg / 1e6).toFixed(3) }} →
                  {{ (l.new_metal_mg / 1e6).toFixed(3) }} kg
                </div>
                <span v-if="c.blocked" class="muted">{{ c.blocked }}</span>
              </td>
            </tr>
          </tbody>
        </table>
        <p class="muted" style="font-size:12px;margin-top:8px">
          当前版本：{{ trial.current_status }} · ¥{{ yuan(trial.current_amount_cents) }}。
          平台只给出每一方的实测结果，不提供平均或折中候选。
        </p>
      </div>

      <div class="panel" v-if="open.adjudications.length">
        <h2>裁定历史</h2>
        <table>
          <thead><tr><th>#</th><th>采用</th><th>可比</th><th>理由</th><th>逐车结算</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="a in open.adjudications" :key="a.id">
              <td>D{{ a.decision_seq }}</td>
              <td>{{ a.adopted }}</td>
              <td>{{ a.comparable ? (a.within_tolerance ? '容差内' : '超容差') : '不可比' }}</td>
              <td style="font-size:12px">{{ a.reason }}</td>
              <td style="font-size:12px">
                <div v-for="l in a.lines" :key="l.trip_id">
                  {{ l.batch_no }}：¥{{ yuan(l.old_amount_cents) }} → ¥{{ yuan(l.new_amount_cents) }}，
                  差额 ¥{{ yuan(l.delta_cents) }}（{{ l.settlement === 'recovery' ? '已付追补' : l.settlement === 'rebuild' ? '未付重建' : l.settlement }}）
                </div>
              </td>
              <td><span class="badge gray">{{ a.status }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="panel" v-if="canQuality && open.open_rereview">
        <h2>重新复核</h2>
        <p class="warnbox">
          裁定后新增了报告，已按“不能覆盖旧决定”提出重新复核：
          {{ open.open_rereview.reason }}
        </p>
        <p class="muted" style="font-size:12px">
          请核对新增报告后重新做一次可比性与试算，再提交新的裁定；旧裁定保留在历史中（只会被标记为 superseded）。
        </p>
      </div>

      <div class="panel" v-if="canQuality && (open.status !== 'settled' || open.open_rereview)">
        <h2>差异裁定</h2>
        <div class="grid cols-3">
          <div class="field"><label>采用结果</label>
            <select v-model="adjForm.adopted">
              <option value="supplier">供应商报告</option>
              <option value="plant">厂内报告</option>
              <option value="external">外送检测（须已揭示）</option>
              <option value="none">暂不采用（转待补证）</option>
            </select>
          </div>
          <div class="field"><label>承认不可比仍要采用</label>
            <select v-model="adjForm.acknowledge_incomparable"><option :value="false">否</option><option :value="true">是</option></select>
          </div>
          <div class="field"><label>理由(必填)</label><input v-model="adjForm.reason" placeholder="说明采用范围与理由" /></div>
        </div>
        <button :disabled="busy" @click="adjudicate">提交裁定</button>
        <p class="muted" style="font-size:12px;margin-top:8px">
          裁定只作用于本车；已付款按差额形成追补/追回，未付款重建付款依据。裁定后再补报告只会提出重新复核，不会覆盖本决定。
        </p>
      </div>
    </template>
  </div>
</template>
