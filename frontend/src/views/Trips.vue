<script setup>
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, getUser, kg } from '../api/client.js'

const router = useRouter()
const trips = ref([])
const suppliers = ref([])
const contracts = ref([])
const error = ref('')
const form = ref({ supplier_id: '', contract_id: '', plate_no: '', driver: '', note: '' })
const user = getUser()
const canCreate = ['purchasing', 'admin'].includes(user?.role)

async function load() {
  try {
    [trips.value, suppliers.value, contracts.value] = await Promise.all([
      api.get('/api/trips'), api.get('/api/suppliers'), api.get('/api/contracts')
    ])
  } catch (e) { error.value = e.message }
}
onMounted(load)

async function create() {
  error.value = ''
  try {
    const t = await api.post('/api/trips', {
      supplier_id: Number(form.value.supplier_id),
      contract_id: Number(form.value.contract_id),
      plate_no: form.value.plate_no,
      driver: form.value.driver,
      note: form.value.note,
      arrival_at: new Date().toISOString(),
    })
    form.value = { supplier_id: '', contract_id: '', plate_no: '', driver: '', note: '' }
    await load()
    router.push(`/trips/${t.id}`)
  } catch (e) { error.value = e.message }
}
</script>

<template>
  <div>
    <h1>车次与来料</h1>
    <p class="lead">同一供应商一天送来几车，每车建立独立稳定批次；化验单必须绑定车次与取样位置。</p>
    <div v-if="error" class="err">{{ error }}</div>

    <div v-if="canCreate" class="panel">
      <h2>新建车次</h2>
      <div class="grid cols-4">
        <div class="field">
          <label>供应商</label>
          <select v-model="form.supplier_id">
            <option value="" disabled>选择</option>
            <option v-for="s in suppliers" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>合同</label>
          <select v-model="form.contract_id">
            <option value="" disabled>选择</option>
            <option v-for="c in contracts" :key="c.id" :value="c.id">{{ c.code }} · {{ c.name }}</option>
          </select>
        </div>
        <div class="field"><label>车牌</label><input v-model="form.plate_no" placeholder="沪A-00000" /></div>
        <div class="field"><label>司机</label><input v-model="form.driver" /></div>
      </div>
      <div class="field"><label>备注</label><input v-model="form.note" /></div>
      <button @click="create">建立批次并录入</button>
    </div>

    <div class="panel">
      <table>
        <thead><tr><th>批次号</th><th>供应商</th><th>合同</th><th>车牌</th><th>到达时间</th><th></th></tr></thead>
        <tbody>
          <tr v-for="t in trips" :key="t.id">
            <td class="mono">{{ t.batch_no }}</td>
            <td>{{ t.supplier_name }}</td>
            <td class="mono">{{ t.contract_code }}</td>
            <td>{{ t.plate_no }}</td>
            <td class="muted">{{ new Date(t.arrival_at).toLocaleString('zh-CN') }}</td>
            <td><router-link :to="`/trips/${t.id}`">打开</router-link></td>
          </tr>
          <tr v-if="!trips.length"><td colspan="6" class="muted">暂无车次</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
