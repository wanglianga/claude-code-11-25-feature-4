<script setup>
import { ref, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { api, getUser, clearSession } from './api/client.js'

const router = useRouter()
const route = useRoute()
const user = ref(getUser())
const isLogin = computed(() => route.path === '/login')

const roleLabel = {
  purchasing: '采购', yard: '料场', lab: '化验', quality: '质量', finance: '财务', admin: '管理员'
}

async function logout() {
  try { await api.post('/api/logout') } catch {}
  clearSession()
  user.value = null
  router.push('/login')
}

const nav = [
  { to: '/', label: '总览' },
  { to: '/trips', label: '车次与来料' },
  { to: '/stock', label: '料堆与追溯' },
  { to: '/movements', label: '转移单' },
  { to: '/reviews', label: '封样复核' },
  { to: '/payables', label: '应付款' },
  { to: '/reconciliation', label: '数量金额核对' },
]
</script>

<template>
  <div v-if="isLogin"><router-view /></div>
  <div v-else class="shell">
    <aside class="sidebar">
      <div class="brand">
        <span class="brand-mark">RT</span>
        <div>
          <div class="brand-title">再生金属追溯</div>
          <div class="brand-sub">来料 · 料堆 · 结算</div>
        </div>
      </div>
      <nav>
        <router-link v-for="n in nav" :key="n.to" :to="n.to" class="nav-link" exact-active-class="active">
          {{ n.label }}
        </router-link>
      </nav>
      <div class="sidebar-foot" v-if="user">
        <div class="who"><strong>{{ user.display_name }}</strong></div>
        <div class="role">{{ roleLabel[user.role] || user.role }}</div>
        <button class="ghost small" @click="logout">退出</button>
      </div>
    </aside>
    <main class="content">
      <router-view :key="$route.fullPath" />
    </main>
  </div>
</template>
