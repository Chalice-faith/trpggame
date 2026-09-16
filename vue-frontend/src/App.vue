<script setup lang="ts">
import { watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'
import { useFriendsStore } from '@/stores/friends'
import { useIMStore } from '@/stores/im'
import { useGroupsStore } from '@/stores/groups'

const authStore = useAuthStore()
const chatStore = useChatStore()
const friendsStore = useFriendsStore()
const imStore = useIMStore()
const groupsStore = useGroupsStore()

watch(
  () => authStore.isLoggedIn,
  (loggedIn) => {
    if (loggedIn) {
      void imStore.connect()
    } else {
      imStore.disconnect()
      chatStore.clear()
      friendsStore.clear()
      groupsStore.clear()
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
