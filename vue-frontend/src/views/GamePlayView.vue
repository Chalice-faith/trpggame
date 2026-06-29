<script setup lang="ts">
// TODO: Phase 1 M1.5 实现游戏主界面
// 包含：NarrativePanel, ActionInput, PlayerStatusSidebar, GameToolbar, DiceAnimation
</script>

<template>
  <div class="game-play-view">
    <aside class="sidebar">
      <!-- TODO: PlayerStatusSidebar -->
    </aside>
    <main class="main-area">
      <!-- TODO: NarrativePanel + ChatBubble -->
      <!-- TODO: ActionInput -->
    </main>
    <div class="toolbar">
      <!-- TODO: GameToolbar -->
    </div>
  </div>
</template>

<style scoped>
.game-play-view {
  display: grid;
  grid-template-columns: 280px 1fr;
  grid-template-rows: 1fr auto;
  height: 100vh;
}
.sidebar {
  grid-row: 1 / 3;
  border-right: 1px solid #333;
}
.main-area {
  display: flex;
  flex-direction: column;
}
.toolbar {
  grid-column: 2;
}
</style>
