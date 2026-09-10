<script setup lang="ts">
import { watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useFriendsStore } from '@/stores/friends'
import { useIMStore } from '@/stores/im'

const authStore = useAuthStore()
const friendsStore = useFriendsStore()
const imStore = useIMStore()

watch(
  () => authStore.isLoggedIn,
  (loggedIn) => {
    if (loggedIn) {
      void imStore.connect()
    } else {
      imStore.disconnect()
      friendsStore.clear()
    }
  },
  { immediate: true }
)
</script>

<template>
  <router-view />
</template>

<style>
body {
  margin: 0;
  font-family: 'Helvetica Neue', Arial, 'PingFang SC', 'Microsoft YaHei', sans-serif;
  background-color: #1a1a2e;
  color: #e0e0e0;
}
</style>
