<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ArrowLeft,
  ChatDotRound,
  Edit,
  Plus,
  Refresh,
  UserFilled
} from '@element-plus/icons-vue'
import type { GroupMember, GroupRole, GroupSummary } from '@/api/groups'
import { useAuthStore } from '@/stores/auth'
import { useFriendsStore } from '@/stores/friends'
import { useGroupsStore } from '@/stores/groups'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const friendsStore = useFriendsStore()
const groupsStore = useGroupsStore()

const createVisible = ref(false)
const editVisible = ref(false)
const inviteVisible = ref(false)
const createForm = reactive({ name: '', avatar_url: '' })
const editForm = reactive({ name: '', avatar_url: '' })
const selectedFriendIds = ref<number[]>([])

const roleCopy: Record<GroupRole, string> = {
  owner: '群主',
  admin: '管理员',
  member: '成员'
}

const currentRole = computed(() => groupsStore.selectedGroup?.current_user_role)
const canInvite = computed(() => currentRole.value === 'owner' || currentRole.value === 'admin')
const canEdit = computed(() => currentRole.value === 'owner')
const existingMemberIds = computed(
  () => new Set(groupsStore.selectedMembers.map(({ user }) => user.id))
)
const inviteCandidates = computed(() =>
  friendsStore.sortedFriends.filter(({ peer }) => !existingMemberIds.value.has(peer.id))
)
const remainingCapacity = computed(() =>
  Math.max(0, 50 - (groupsStore.selectedGroup?.member_count ?? 0))
)

function routeGroupId() {
  const raw = route.params.groupId
  const value = Number(Array.isArray(raw) ? raw[0] : raw)
  return Number.isSafeInteger(value) && value > 0 ? value : null
}

async function openRouteGroup() {
  const groupId = routeGroupId()
  if (!groupId) {
    groupsStore.selectedGroupId = null
    return
  }
  try {
    await groupsStore.openGroup(groupId)
  } catch (error: any) {
    showError(error, '群组加载失败')
    await router.replace('/groups')
  }
}

async function refresh() {
  try {
    await groupsStore.loadGroups(true)
    await openRouteGroup()
  } catch (error: any) {
    showError(error, groupsStore.errorMessage || '群组数据加载失败')
    return
  }
  try {
    await friendsStore.fetchFriends()
  } catch {
    ElMessage.warning('好友列表加载失败，暂时无法邀请新成员')
  }
}

async function selectGroup(group: GroupSummary) {
  if (routeGroupId() === group.id) {
    await groupsStore.openGroup(group.id)
    return
  }
  await router.push(`/groups/${group.id}`)
}

async function submitCreate() {
  if (!createForm.name.trim()) return
  try {
    const group = await groupsStore.create(createForm.name, createForm.avatar_url)
    createVisible.value = false
    createForm.name = ''
    createForm.avatar_url = ''
    await router.push(`/groups/${group.id}`)
    ElMessage.success('群组已创建')
  } catch (error: any) {
    showError(error, '创建群组失败')
  }
}

function openEdit() {
  const group = groupsStore.selectedGroup
  if (!group) return
  editForm.name = group.name
  editForm.avatar_url = group.avatar_url
  editVisible.value = true
}

async function submitEdit() {
  if (!editForm.name.trim()) return
  try {
    await groupsStore.updateProfile({
      name: editForm.name.trim(),
      avatar_url: editForm.avatar_url.trim()
    })
    editVisible.value = false
    ElMessage.success('群资料已更新')
  } catch (error: any) {
    showError(error, '更新群资料失败')
  }
}

function openInvite() {
  selectedFriendIds.value = []
  inviteVisible.value = true
}

async function submitInvite() {
  if (!selectedFriendIds.value.length) return
  try {
    await groupsStore.invite(selectedFriendIds.value)
    inviteVisible.value = false
    ElMessage.success('成员已加入群组')
  } catch (error: any) {
    showError(error, '邀请成员失败')
  }
}

function canRemove(member: GroupMember) {
  if (member.role === 'owner' || member.user.id === authStore.user?.id) return false
  return currentRole.value === 'owner' ||
    (currentRole.value === 'admin' && member.role === 'member')
}

async function toggleRole(member: GroupMember) {
  const nextRole = member.role === 'admin' ? 'member' : 'admin'
  try {
    await groupsStore.setRole(member.user.id, nextRole)
    ElMessage.success(nextRole === 'admin' ? '已设为管理员' : '已取消管理员')
  } catch (error: any) {
    showError(error, '调整成员角色失败')
  }
}

async function removeMember(member: GroupMember) {
  try {
    await ElMessageBox.confirm(
      `确定将“${userName(member.user)}”移出群组吗？`,
      '移除成员',
      { type: 'warning', confirmButtonText: '移除', cancelButtonText: '取消' }
    )
    await groupsStore.remove(member.user.id)
    ElMessage.success('成员已移除')
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '移除成员失败')
  }
}

async function transferOwner(member: GroupMember) {
  try {
    await ElMessageBox.confirm(
      `确定将群主转让给“${userName(member.user)}”吗？`,
      '转让群主',
      { type: 'warning', confirmButtonText: '确认转让', cancelButtonText: '取消' }
    )
    await groupsStore.transfer(member.user.id)
    ElMessage.success('群主已转让')
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '转让群主失败')
  }
}

async function leaveGroup() {
  const group = groupsStore.selectedGroup
  const currentUserId = authStore.user?.id
  if (!group || !currentUserId || currentRole.value === 'owner') return
  try {
    await ElMessageBox.confirm(
      `退出“${group.name}”后将立即无法查看历史消息，确定退出吗？`,
      '退出群组',
      { type: 'warning', confirmButtonText: '退出', cancelButtonText: '取消' }
    )
    await groupsStore.remove(currentUserId)
    await router.replace('/groups')
    ElMessage.success('已退出群组')
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '退出群组失败')
  }
}

function userName(user: { nickname: string; username: string }) {
  return user.nickname.trim() || user.username
}

function avatarText(value: { name?: string; nickname?: string; username?: string }) {
  const label = value.name || value.nickname?.trim() || value.username || '?'
  return [...label][0]?.toUpperCase() || '?'
}

function showError(error: any, fallback: string) {
  const code = error?.response?.data?.code
  const known: Record<number, string> = {
    1801: '群组不存在或你已不在群内',
    1802: '当前角色没有此操作权限',
    1803: '只能邀请当前好友',
    1804: '群成员已达到上限',
    1805: '群主需先转让群主身份',
    1806: '群信息已变化，页面已刷新，请重试'
  }
  ElMessage.error(known[code] || error?.response?.data?.message || fallback)
}

onMounted(refresh)
watch(() => route.params.groupId, openRouteGroup)
</script>

<template>
  <main class="groups-view">
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark"><UserFilled /></span>
        <div>
          <strong>冒险者群组</strong>
          <small>组织同伴、分配权限并开始群聊</small>
        </div>
      </div>
      <div class="topbar-actions">
        <el-button :icon="ChatDotRound" @click="router.push('/chat')">消息</el-button>
        <el-button :icon="ArrowLeft" @click="router.push('/dashboard')">剧本库</el-button>
      </div>
    </header>

    <section class="group-shell">
      <aside class="group-list">
        <div class="list-heading">
          <div>
            <p class="eyebrow">GROUPS</p>
            <h1>我的群组</h1>
          </div>
          <div>
            <el-button text :icon="Refresh" :loading="groupsStore.loading" @click="refresh" />
            <el-button type="primary" circle :icon="Plus" data-testid="create-group-open" @click="createVisible = true" />
          </div>
        </div>
        <el-alert v-if="groupsStore.errorMessage" type="error" :closable="false" :title="groupsStore.errorMessage" />
        <div v-if="!groupsStore.loading && !groupsStore.groups.length" class="empty-state">
          还没有群组，创建一个并邀请好友加入吧。
        </div>
        <button
          v-for="group in groupsStore.groups"
          :key="group.id"
          type="button"
          class="group-row"
          :class="{ active: groupsStore.selectedGroupId === group.id }"
          data-testid="group-row"
          @click="selectGroup(group)"
        >
          <el-avatar :size="46" :src="group.avatar_url">{{ avatarText(group) }}</el-avatar>
          <span>
            <strong>{{ group.name }}</strong>
            <small>{{ group.member_count }} 人 · {{ group.current_user_role ? roleCopy[group.current_user_role] : '' }}</small>
          </span>
        </button>
        <el-button v-if="groupsStore.nextCursor" text class="load-more" @click="groupsStore.loadMoreGroups">加载更多</el-button>
      </aside>

      <section v-if="groupsStore.selectedGroup" class="group-detail">
        <header class="detail-heading">
          <el-avatar :size="58" :src="groupsStore.selectedGroup.avatar_url">
            {{ avatarText(groupsStore.selectedGroup) }}
          </el-avatar>
          <div>
            <h2>{{ groupsStore.selectedGroup.name }}</h2>
            <p>{{ groupsStore.selectedGroup.member_count }} / 50 人 · {{ currentRole ? roleCopy[currentRole] : '' }}</p>
          </div>
          <el-button type="primary" :icon="ChatDotRound" @click="router.push(`/chat/${groupsStore.selectedGroup.conversation_id}`)">进入群聊</el-button>
          <el-button v-if="canEdit" :icon="Edit" data-testid="edit-group-open" @click="openEdit">编辑资料</el-button>
          <el-button v-if="canInvite" :icon="Plus" data-testid="invite-member-open" @click="openInvite">邀请好友</el-button>
          <el-button v-if="currentRole !== 'owner'" type="danger" plain @click="leaveGroup">退出群组</el-button>
        </header>

        <div class="member-heading">
          <div><p class="eyebrow">MEMBERS</p><h3>群成员</h3></div>
          <el-tag round>{{ groupsStore.selectedMembers.length }}</el-tag>
        </div>
        <div v-loading="groupsStore.loadingMembers" class="member-list">
          <article v-for="member in groupsStore.selectedMembers" :key="member.id" class="member-row" data-testid="group-member-row">
            <el-avatar :size="42" :src="member.user.avatar_url">{{ avatarText(member.user) }}</el-avatar>
            <div class="member-copy">
              <strong>{{ userName(member.user) }}</strong>
              <small>@{{ member.user.username }} · ID {{ member.user.id }}</small>
            </div>
            <el-tag :type="member.role === 'owner' ? 'danger' : member.role === 'admin' ? 'warning' : 'info'">{{ roleCopy[member.role] }}</el-tag>
            <template v-if="currentRole === 'owner' && member.role !== 'owner'">
              <el-button text data-testid="toggle-role-button" @click="toggleRole(member)">{{ member.role === 'admin' ? '取消管理员' : '设为管理员' }}</el-button>
              <el-button text @click="transferOwner(member)">转让群主</el-button>
            </template>
            <el-button v-if="canRemove(member)" text type="danger" data-testid="remove-member-button" @click="removeMember(member)">移除</el-button>
          </article>
        </div>
        <el-alert v-if="currentRole === 'owner'" class="owner-note" type="info" :closable="false" title="群主不能直接退出；请先将群主转让给其他成员。" />
      </section>

      <section v-else class="group-detail no-selection">
        <UserFilled />
        <h2>选择一个群组</h2>
        <p>可查看成员、管理权限或进入群聊。</p>
      </section>
    </section>

    <el-dialog v-model="createVisible" title="创建群组" width="min(92vw, 480px)" :close-on-click-modal="!groupsStore.mutating">
      <el-form label-position="top">
        <el-form-item label="群名称"><el-input v-model="createForm.name" maxlength="80" show-word-limit data-testid="create-group-name" /></el-form-item>
        <el-form-item label="头像 URL（可选）"><el-input v-model="createForm.avatar_url" maxlength="2048" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="createVisible = false">取消</el-button><el-button type="primary" :loading="groupsStore.mutating" :disabled="!createForm.name.trim()" data-testid="create-group-submit" @click="submitCreate">创建</el-button></template>
    </el-dialog>

    <el-dialog v-model="editVisible" title="编辑群资料" width="min(92vw, 480px)">
      <el-form label-position="top">
        <el-form-item label="群名称"><el-input v-model="editForm.name" maxlength="80" show-word-limit /></el-form-item>
        <el-form-item label="头像 URL"><el-input v-model="editForm.avatar_url" maxlength="2048" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="editVisible = false">取消</el-button><el-button type="primary" :loading="groupsStore.mutating" :disabled="!editForm.name.trim()" @click="submitEdit">保存</el-button></template>
    </el-dialog>

    <el-dialog v-model="inviteVisible" title="邀请好友" width="min(92vw, 560px)">
      <p class="dialog-copy">还可邀请 {{ remainingCapacity }} 人；单次最多选择 20 人。</p>
      <el-checkbox-group v-model="selectedFriendIds" class="friend-options" :max="Math.min(20, remainingCapacity)">
        <el-checkbox v-for="item in inviteCandidates" :key="item.peer.id" :value="item.peer.id" border>
          {{ userName(item.peer) }}（@{{ item.peer.username }}）
        </el-checkbox>
      </el-checkbox-group>
      <div v-if="!inviteCandidates.length" class="empty-state compact">没有可邀请的好友</div>
      <template #footer><el-button @click="inviteVisible = false">取消</el-button><el-button type="primary" :loading="groupsStore.mutating" :disabled="!selectedFriendIds.length" data-testid="invite-member-submit" @click="submitInvite">邀请 {{ selectedFriendIds.length }} 人</el-button></template>
    </el-dialog>
  </main>
</template>

<style scoped>
.groups-view { min-height: 100vh; padding: 0 34px 36px; box-sizing: border-box; color: #e9e9f1; text-align: left; background: radial-gradient(circle at 8% 2%, rgba(63,195,172,.13), transparent 28%), radial-gradient(circle at 92% 10%, rgba(233,69,96,.12), transparent 25%), #141424; }
.topbar,.brand,.topbar-actions,.list-heading,.detail-heading,.member-heading,.member-row { display: flex; align-items: center; }
.topbar { min-height: 78px; justify-content: space-between; border-bottom: 1px solid rgba(255,255,255,.08); }
.brand { gap: 12px; }.brand strong,.brand small { display: block; }.brand small { margin-top: 3px; color: #85859d; }.brand-mark { display: grid; width: 42px; height: 42px; place-items: center; border-radius: 12px; background: linear-gradient(135deg,#3fc3ac,#2a7e87); }.brand-mark svg { width: 21px; }.topbar-actions { gap: 10px; }
.group-shell { display: grid; grid-template-columns: minmax(260px,320px) minmax(0,1fr); min-height: calc(100vh - 114px); margin-top: 18px; overflow: hidden; border: 1px solid rgba(255,255,255,.08); border-radius: 18px; background: rgba(25,25,44,.96); }
.group-list { border-right: 1px solid rgba(255,255,255,.08); background: rgba(17,17,32,.62); }.list-heading { min-height: 74px; justify-content: space-between; padding: 0 18px; border-bottom: 1px solid rgba(255,255,255,.07); }.list-heading h1,.member-heading h3 { margin: 2px 0 0; }.eyebrow { margin: 0; color: #3fc3ac; font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.group-row { display: flex; width: 100%; min-height: 76px; align-items: center; gap: 11px; padding: 12px 15px; border: 0; border-bottom: 1px solid rgba(255,255,255,.055); color: inherit; text-align: left; cursor: pointer; background: transparent; }.group-row:hover { background: rgba(255,255,255,.035); }.group-row.active { background: linear-gradient(90deg,rgba(63,195,172,.16),rgba(63,195,172,.04)); box-shadow: inset 3px 0 #3fc3ac; }.group-row span { min-width: 0; }.group-row strong,.group-row small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.group-row small { margin-top: 5px; color: #85859b; }.load-more { width: 100%; }
.group-detail { min-width: 0; padding-bottom: 24px; }.detail-heading { min-height: 82px; gap: 12px; padding: 0 22px; border-bottom: 1px solid rgba(255,255,255,.07); }.detail-heading div { min-width: 0; flex: 1; }.detail-heading h2,.detail-heading p { margin: 0; }.detail-heading p { margin-top: 4px; color: #85859d; font-size: 13px; }.member-heading { justify-content: space-between; padding: 24px 28px 12px; }.member-list { padding: 0 28px; }.member-row { min-height: 66px; gap: 11px; border-bottom: 1px solid rgba(255,255,255,.06); }.member-copy { min-width: 0; flex: 1; }.member-copy strong,.member-copy small { display: block; }.member-copy small { margin-top: 3px; color: #85859d; }.owner-note { width: auto; margin: 18px 28px 0; }.empty-state { padding: 56px 18px; color: #77778e; text-align: center; }.empty-state.compact { padding: 24px; }.no-selection { display: grid; place-content: center; justify-items: center; color: #6f6f84; text-align: center; }.no-selection svg { width: 46px; }.no-selection h2 { margin: 12px 0 4px; }.no-selection p { margin: 0; }.dialog-copy { color: #77778e; }.friend-options { display: grid; max-height: 340px; gap: 8px; overflow-y: auto; }.friend-options .el-checkbox { width: 100%; margin: 0; }
@media (max-width: 900px) { .groups-view { padding: 0 14px 20px; }.group-shell { grid-template-columns: 120px minmax(0,1fr); }.list-heading { padding: 0 8px; }.list-heading h1 { font-size: 17px; }.list-heading .eyebrow,.list-heading .el-button:first-of-type,.group-row small { display: none; }.group-row { flex-direction: column; padding: 9px 5px; text-align: center; }.detail-heading { align-items: flex-start; flex-wrap: wrap; padding: 14px; }.detail-heading div { min-width: calc(100% - 75px); }.member-list,.member-heading { padding-right: 14px; padding-left: 14px; }.member-row { flex-wrap: wrap; padding: 8px 0; }.member-copy { min-width: calc(100% - 66px); } }
</style>
