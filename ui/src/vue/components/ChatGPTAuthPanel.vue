<template>
  <section v-if="status" class="chatgpt-auth-panel" aria-labelledby="chatgpt-auth-title">
    <div class="chatgpt-auth-copy">
      <div class="chatgpt-auth-heading">
        <span id="chatgpt-auth-title">ChatGPT</span>
        <span :class="`chatgpt-auth-state ${status.authenticated ? 'connected' : status.mode}`">
          {{ statusLabel }}
        </span>
      </div>
      <p v-if="status.mode === 'pillar'">
        Your ChatGPT subscription is managed by Pillar and automatically shared with Shelley.
      </p>
      <p v-else-if="status.authenticated">
        <span v-if="status.account_id">Account {{ status.account_id }}</span>
        <span v-if="status.expires_at">
          · Session renews automatically after {{ formattedExpiry }}</span
        >
      </p>
      <p v-else>
        Use your ChatGPT subscription for Codex models. Sign-in uses a browser redirect with PKCE.
      </p>
      <p v-if="panelError || status.error" class="chatgpt-auth-error">
        {{ panelError || status.error }}
      </p>
      <template v-if="flow && status.mode === 'standalone' && !status.authenticated">
        <p class="chatgpt-auth-flow-note">
          Complete sign-in in the browser. This panel will update automatically.
        </p>
        <a :href="flow.authorization_url" target="_blank" rel="noopener noreferrer">
          Open sign-in again
        </a>
        <p v-if="!flow.callback_listener" class="chatgpt-auth-flow-note">
          Shelley could not listen on localhost:1455. After the browser redirects, paste the full
          callback URL below.
        </p>
        <form class="chatgpt-auth-callback" @submit.prevent="completeSignIn">
          <input
            v-model="callbackUrl"
            type="url"
            inputmode="url"
            autocomplete="off"
            placeholder="http://localhost:1455/auth/callback?code=..."
            aria-label="OAuth callback URL"
          />
          <Button
            type="submit"
            size="small"
            severity="secondary"
            :disabled="busy || !callbackUrl.trim()"
            label="Complete"
          />
        </form>
      </template>
    </div>
    <div class="chatgpt-auth-actions">
      <a
        v-if="status.mode === 'pillar' && status.reauth_url"
        :href="status.reauth_url"
        target="_blank"
        rel="noopener noreferrer"
        class="chatgpt-auth-link-button"
      >
        Re-authenticate
      </a>
      <Button
        v-else-if="status.mode === 'standalone' && status.authenticated"
        size="small"
        severity="secondary"
        :disabled="busy"
        label="Sign out"
        @click="signOut"
      />
      <Button
        v-else-if="status.mode === 'standalone'"
        size="small"
        :disabled="busy"
        :label="busy ? 'Starting…' : 'Connect ChatGPT'"
        @click="startSignIn"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import Button from "primevue/button";
import { api, type ChatGPTAuthFlow, type ChatGPTAuthStatus } from "../../services/api";

const props = defineProps<{ active: boolean }>();
const emit = defineEmits<{ (e: "modelsChanged"): void }>();

const status = ref<ChatGPTAuthStatus | null>(null);
const flow = ref<ChatGPTAuthFlow | null>(null);
const callbackUrl = ref("");
const panelError = ref("");
const busy = ref(false);
let pollTimer: number | null = null;
let polling = false;

const statusLabel = computed(() => {
  if (status.value?.mode === "pillar") return "Managed by Pillar";
  return status.value?.authenticated ? "Connected" : "Not connected";
});

const formattedExpiry = computed(() => {
  if (!status.value?.expires_at) return "";
  const date = new Date(status.value.expires_at);
  return Number.isNaN(date.valueOf())
    ? status.value.expires_at
    : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
});

async function loadStatus() {
  try {
    panelError.value = "";
    status.value = await api.getChatGPTAuthStatus();
  } catch (err) {
    status.value = null;
    panelError.value = err instanceof Error ? err.message : "Failed to load ChatGPT status";
  }
}

function stopPolling() {
  if (pollTimer !== null) window.clearInterval(pollTimer);
  pollTimer = null;
}

function startPolling() {
  stopPolling();
  pollTimer = window.setInterval(async () => {
    if (polling) return;
    polling = true;
    try {
      const next = await api.getChatGPTAuthStatus();
      status.value = next;
      if (next.authenticated) {
        flow.value = null;
        stopPolling();
        emit("modelsChanged");
      }
    } catch {
      // The explicit flow UI remains available if a transient poll fails.
    } finally {
      polling = false;
    }
  }, 1000);
}

async function startSignIn() {
  const popup = window.open("about:blank", "shelley-chatgpt-auth");
  if (popup) popup.opener = null;
  try {
    busy.value = true;
    panelError.value = "";
    flow.value = await api.startChatGPTAuth();
    callbackUrl.value = "";
    if (popup) popup.location.href = flow.value.authorization_url;
    startPolling();
  } catch (err) {
    popup?.close();
    panelError.value = err instanceof Error ? err.message : "Failed to start ChatGPT sign-in";
  } finally {
    busy.value = false;
  }
}

async function completeSignIn() {
  try {
    busy.value = true;
    panelError.value = "";
    status.value = await api.completeChatGPTAuth(callbackUrl.value.trim());
    flow.value = null;
    callbackUrl.value = "";
    stopPolling();
    emit("modelsChanged");
  } catch (err) {
    panelError.value = err instanceof Error ? err.message : "Failed to complete ChatGPT sign-in";
  } finally {
    busy.value = false;
  }
}

async function signOut() {
  try {
    busy.value = true;
    panelError.value = "";
    await api.logoutChatGPT();
    await loadStatus();
    emit("modelsChanged");
  } catch (err) {
    panelError.value = err instanceof Error ? err.message : "Failed to sign out of ChatGPT";
  } finally {
    busy.value = false;
  }
}

watch(
  () => props.active,
  (active) => {
    if (active) void loadStatus();
    else stopPolling();
  },
  { immediate: true },
);

onBeforeUnmount(stopPolling);
</script>
