import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '@/composables/axios'

export interface User {
  id: number
  username: string
  email: string
  nickname: string
  avatar_url: string
}

export const useAuthStore = defineStore('auth', () => {
  // ---- state ----
  const user = ref<User | null>(null)
  const accessToken = ref<string | null>(localStorage.getItem('access_token'))
  const refreshToken = ref<string | null>(localStorage.getItem('refresh_token'))

  // ---- getters ----
  const isLoggedIn = computed(() => !!accessToken.value && !!user.value)

  // ---- actions ----
  function setTokens(access: string, refresh: string) {
    accessToken.value = access
    refreshToken.value = refresh
    localStorage.setItem('access_token', access)
    localStorage.setItem('refresh_token', refresh)
  }

  function clearTokens() {
    accessToken.value = null
    refreshToken.value = null
    localStorage.removeItem('access_token')
    localStorage.removeItem('refresh_token')
  }

  async function login(username: string, password: string) {
    const res = await api.post('/api/v1/auth/login', { username, password })
    const { access_token, refresh_token, user_id, username: uname } = res.data.data
    setTokens(access_token, refresh_token)
    user.value = { id: user_id, username: uname } as User
    await fetchProfile()
  }

  async function register(username: string, email: string, password: string) {
    const res = await api.post('/api/v1/auth/register', { username, email, password })
    const { access_token, refresh_token, user_id, username: uname } = res.data.data
    setTokens(access_token, refresh_token)
    user.value = { id: user_id, username: uname } as User
    await fetchProfile()
  }

  async function fetchProfile() {
    const res = await api.get('/api/v1/users/me')
    user.value = res.data.data
  }

  async function refreshAccessToken() {
    if (!refreshToken.value) return
    try {
      const res = await api.post('/api/v1/auth/refresh', {
        refresh_token: refreshToken.value
      })
      const { access_token, refresh_token } = res.data.data
      setTokens(access_token, refresh_token)
    } catch {
      logout()
    }
  }

  function logout() {
    clearTokens()
    user.value = null
  }

  return { user, accessToken, refreshToken, isLoggedIn, login, register, fetchProfile, refreshAccessToken, logout }
})
