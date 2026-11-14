import { createRouter, createWebHistory } from 'vue-router'
import { getToken } from './api/client.js'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Trips from './views/Trips.vue'
import TripDetail from './views/TripDetail.vue'
import Stock from './views/Stock.vue'
import Payables from './views/Payables.vue'
import Reconciliation from './views/Reconciliation.vue'
import Movements from './views/Movements.vue'
import Reviews from './views/Reviews.vue'

const routes = [
  { path: '/login', component: Login, meta: { public: true } },
  { path: '/', component: Dashboard },
  { path: '/trips', component: Trips },
  { path: '/trips/:id', component: TripDetail },
  { path: '/stock', component: Stock },
  { path: '/payables', component: Payables },
  { path: '/reconciliation', component: Reconciliation },
  { path: '/movements', component: Movements },
  { path: '/reviews', component: Reviews },
]

const router = createRouter({ history: createWebHistory(), routes })
router.beforeEach((to) => {
  if (!to.meta.public && !getToken()) return '/login'
  if (to.path === '/login' && getToken()) return '/'
})
export default router
