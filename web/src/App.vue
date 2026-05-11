<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView } from 'vue-router'

const { locale, t } = useI18n()

const localeLabel = computed(() => (locale.value === 'zh-CN' ? '中文' : 'English'))

function toggleLocale() {
  locale.value = locale.value === 'zh-CN' ? 'en' : 'zh-CN'
}
</script>

<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">KubeMorph</div>
      <nav class="nav">
        <RouterLink to="/clusters">{{ t('nav.clusters') }}</RouterLink>
        <RouterLink to="/applications">{{ t('nav.applications') }}</RouterLink>
        <RouterLink to="/optimizations">{{ t('nav.optimizations') }}</RouterLink>
      </nav>
      <button class="lang" type="button" @click="toggleLocale">
        {{ t('nav.language') }}: {{ localeLabel }}
      </button>
    </header>

    <main class="content">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.app {
  min-height: 100vh;
  color: #111827;
}
.topbar {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 12px 16px;
  background: rgba(255, 255, 255, 0.78);
  border-bottom: 1px solid #e5e7eb;
  backdrop-filter: blur(10px);
}
.brand {
  font-weight: 700;
  letter-spacing: 0.08em;
}
.nav {
  display: flex;
  gap: 12px;
  flex: 1;
}
.nav a {
  color: #374151;
  text-decoration: none;
  padding: 6px 10px;
  border-radius: 8px;
}
.nav a.router-link-active {
  background: rgba(46, 140, 255, 0.12);
  color: #0b3a71;
}
.lang {
  border: 1px solid #e5e7eb;
  background: rgba(255, 255, 255, 0.8);
  border-radius: 10px;
  padding: 6px 10px;
  cursor: pointer;
}
.content {
  padding: 16px;
  max-width: 1100px;
  margin: 0 auto;
}
</style>
