import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

// 懒加载视图
const LoginView = () => import('@/views/LoginView.vue')
const RegisterView = () => import('@/views/RegisterView.vue')
const DashboardView = () => import('@/views/DashboardView.vue')
const ScriptDetailView = () => import('@/views/ScriptDetailView.vue')
const GameSoloView = () => import('@/views/GameSoloView.vue')
const GamePlayView = () => import('@/views/GamePlayView.vue')

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    redirect: '/dashboard'
  },
  {
    path: '/login',
    name: 'Login',
    component: LoginView,
    meta: { guest: true }
  },
  {
    path: '/register',
    name: 'Register',
    component: RegisterView,
    meta: { guest: true }
  },
  {
    path: '/dashboard',
    name: 'Dashboard',
    component: DashboardView,
    meta: { requiresAuth: true }
  },
  {
    path: '/scripts/:id',
    name: 'ScriptDetail',
    component: ScriptDetailView,
    meta: { requiresAuth: true }
  },
  {
    path: '/game/solo/:id',
    name: 'GameSolo',
    component: GameSoloView,
    meta: { requiresAuth: true }
  },
  {
    path: '/game/play/:id',
    name: 'GamePlay',
    component: GamePlayView,
    meta: { requiresAuth: true }
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

// 路由守卫 — 鉴权检查
router.beforeEach(async (to) => {
  const authStore = useAuthStore()

  if (authStore.accessToken && !authStore.user) {
    try {
      await authStore.fetchProfile()
    } catch {
      authStore.logout()
    }
  }

  if (to.meta.requiresAuth && !authStore.isLoggedIn) {
    return { name: 'Login', query: { redirect: to.fullPath } }
  }
  if (to.meta.guest && authStore.isLoggedIn) {
    return { name: 'Dashboard' }
  }

  return true
})

export default router
