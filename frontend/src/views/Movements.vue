<script setup>
import { ref, onMounted } from 'vue'
import { api, kg } from '../api/client.js'

const list = ref([])
const error = ref('')
onMounted(async () => {
  try { list.value = await api.get('/api/movements') } catch (e) { error.value = e.message }
})
const dirLabel = { out: '转出', in: '转入', loss: '损耗' }
</script>

<template>
  <div>
    <h1>转移单</h1>
    <p class="lead">每次合堆、拆分和出库形成带数量的来源关系；同一单号重放不会重复移动材料。</p>
    <div v-if="error" class="err">{{ error }}</div>
    <div class="panel" v-for="m in list" :key="m.id">
      <div class="row">
        <span class="mono">{{ m.external_ref }}</span>
        <span class="badge gray">{{ m.kind }}</span>
        <span class="spacer"></span>
        <span class="muted">{{ new Date(m.occurred_at).toLocaleString('zh-CN') }}</span>
      </div>
      <p class="muted" style="font-size:12px;margin:6px 0">{{ m.note }}</p>
      <table>
        <thead><tr><th>方向</th><th>批次节点</th><th>堆位</th><th class="num">重量 kg</th><th class="num">金属 kg</th><th>依据</th></tr></thead>
        <tbody>
          <tr v-for="(l, i) in m.lines" :key="i">
            <td>
              <span class="badge" :class="{ danger: l.direction==='loss', gray: l.direction==='in' }">
                {{ dirLabel[l.direction] }}{{ l.loss_class ? ` · ${l.loss_class}` : '' }}
              </span>
            </td>
            <td class="mono">{{ l.lot_key }}</td>
            <td>{{ l.pile_name }}</td>
            <td class="num">{{ kg(l.weight_g) }}</td>
            <td class="num">{{ (l.metal_mg/1e6).toFixed(3) }}</td>
            <td><span class="badge gray">{{ l.basis }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
