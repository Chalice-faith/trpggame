<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { getScript, type ScriptDetail } from '@/api/scripts'
import { startSoloGame } from '@/api/game'
import { useGameStore } from '@/stores/game'

const route = useRoute()
const router = useRouter()
const gameStore = useGameStore()
const script = ref<ScriptDetail | null>(null)
const selectedCharacterId = ref<number | null>(null)
const loading = ref(true)
const starting = ref(false)
const scriptId = computed(() => Number(route.params.id))
const selectedCharacter = computed(() =>
  script.value?.characters?.find((character) => character.id === selectedCharacterId.value)
)

async function loadScript() {
  if (!Number.isInteger(scriptId.value) || scriptId.value <= 0) {
    ElMessage.error('无效的剧本编号')
    loading.value = false
    router.replace('/dashboard')
    return
  }
  try {
    script.value = await getScript(scriptId.value)
    selectedCharacterId.value = script.value.characters?.[0]?.id ?? null
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '剧本加载失败')
  } finally {
    loading.value = false
  }
}

async function startGame() {
  if (!selectedCharacterId.value || starting.value) return
  starting.value = true
  try {
    const result = await startSoloGame(scriptId.value, selectedCharacterId.value)
    gameStore.reset()
    gameStore.setPlayerStatusFromAttributes(selectedCharacter.value?.attributes)
    gameStore.setRoom({
      id: result.room_id,
      script_id: scriptId.value,
      title: script.value?.title || '单人冒险',
      status: result.game_status,
      current_turn: 0,
      round_count: 0
    })
    if (result.opening_narrative?.trim()) {
      gameStore.appendNarrative('gm', result.opening_narrative.trim())
    }
    router.replace(`/game/play/${result.room_id}`)
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '游戏启动失败，请稍后重试')
  } finally {
    starting.value = false
  }
}

onMounted(loadScript)
</script>

<template>
  <div class="game-solo-view">
    <div v-if="loading" class="loading">正在读取剧本角色…</div>
    <template v-else-if="script">
      <p class="eyebrow">NEW SOLO ADVENTURE</p>
      <h1>选择你的角色</h1>
      <p class="subtitle">{{ script.title }} · 开场后将通过实时连接推进叙事</p>
      <div v-if="script.characters?.length" class="character-grid">
        <button
          v-for="character in script.characters"
          :key="character.id"
          type="button"
          :class="['character-card', { selected: selectedCharacterId === character.id }]"
          @click="selectedCharacterId = character.id"
        >
          <span class="avatar">{{ character.name.slice(0, 1) }}</span>
          <span>
            <strong>{{ character.name }}</strong>
            <small>{{ character.description || '暂无角色描述' }}</small>
            <em v-if="character.id === selectedCharacterId">
              {{ Object.keys(character.attributes || {}).length }} 项初始属性
            </em>
          </span>
        </button>
      </div>
      <el-empty v-else description="剧本没有可用角色" />
      <el-button
        type="primary"
        size="large"
        :loading="starting"
        :disabled="!selectedCharacterId"
        @click="startGame"
      >
        开始冒险
      </el-button>
    </template>
  </div>
</template>

<style scoped>
.game-solo-view {
  min-height: 100vh;
  padding: 24px;
  color: #f7f7fb;
  background: linear-gradient(180deg, #141424, #1a1a2e);
  text-align: center;
}
.loading { margin-top: 20vh; color: #9999ad; }
.eyebrow { margin-top: 9vh; color: #e94560; font-size: 11px; letter-spacing: .16em; }
h1 { margin: 12px 0 8px; font-size: clamp(32px, 5vw, 48px); }
.subtitle { color: #9999ad; }
.character-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 14px;
  max-width: 860px;
  margin: 44px auto 28px;
  text-align: left;
}
.character-card {
  display: flex;
  gap: 14px;
  align-items: flex-start;
  padding: 18px;
  border: 1px solid rgba(255, 255, 255, .1);
  border-radius: 14px;
  color: #e8e8f0;
  background: rgba(255, 255, 255, .04);
  cursor: pointer;
  text-align: left;
}
.character-card.selected { border-color: #e94560; background: rgba(233, 69, 96, .12); }
.avatar {
  display: grid;
  flex: 0 0 42px;
  height: 42px;
  place-items: center;
  border-radius: 12px;
  color: #f3b7c1;
  background: rgba(233, 69, 96, .2);
}
.character-card strong, .character-card small { display: block; }
.character-card small { margin-top: 7px; color: #9999ad; line-height: 1.5; }
.character-card em { display: block; margin-top: 8px; color: #e9a23b; font-size: 11px; font-style: normal; }
@media (max-width: 560px) {
  .eyebrow { margin-top: 4vh; }
}
</style>
