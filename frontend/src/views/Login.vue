<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, setSession } from '../api/client.js'

const router = useRouter()
const username = ref('yard')
const password = ref('yard123')
const err = ref('')
const busy = ref(false)

const accounts = [
  ['buyer', 'buyer123', '采购'],
  ['yard', 'yard123', '料场'],
  ['lab', 'lab123', '化验'],
  ['quality', 'quality123', '质量'],
  ['finance', 'finance123', '财务'],
  ['admin', 'admin123', '管理员'],
]

async function login() {
  err.value = ''
  busy.value = true
  try {
    const res = await api.post('/api/login', { username: username.value, password: password.value })
    setSession(res.token, res.user)
    router.push('/')
  } catch (e) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}
function pick(u, p) { username.value = u; password.value = p }
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <div class="brand" style="margin-bottom:18px">
        <span class="brand-mark">RT</span>
        <div>
          <div class="brand-title" style="color:var(--ink)">再生金属来料与料堆追溯平台</div>
          <div class="brand-sub">逐车称重 · 样品品位 · 混料去向 · 供应商应付款</div>
        </div>
      </div>
      <div v-if="err" class="err">{{ err }}</div>
      <div class="field">
        <label>账号</label>
        <input v-model="username" @keyup.enter="login" placeholder="username" />
      </div>
      <div class="field">
        <label>密码</label>
        <input v-model="password" type="password" @keyup.enter="login" placeholder="password" />
      </div>
      <button style="width:100%" :disabled="busy" @click="login">{{ busy ? '登录中…' : '登录' }}</button>
      <div class="demo-accounts">
        <div class="muted" style="font-size:12px;margin-bottom:6px">测试账号（点击填入）</div>
        <div class="pill-row" style="margin:0">
          <button v-for="a in accounts" :key="a[0]" class="subtle" style="font-size:12px;padding:4px 10px"
            @click="pick(a[0], a[1])">{{ a[2] }} · {{ a[0] }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-wrap { min-height: 100vh; display: grid; place-items: center; background: var(--bg); }
.login-card {
  width: 420px; background: #fff; border: 1px solid var(--line); border-radius: 14px; padding: 26px 28px;
  box-shadow: 0 10px 30px rgba(35,48,42,.08);
}
.demo-accounts { margin-top: 18px; border-top: 1px solid var(--line); padding-top: 14px; }
</style>
