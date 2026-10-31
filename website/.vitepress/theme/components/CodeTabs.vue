<script setup lang="ts">
import { ref } from 'vue'

defineProps<{
  items: { id: string; label: string; code: string; note?: string }[]
  label?: string
}>()

const active = ref(0)
const copied = ref(false)

const copy = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    setTimeout(() => (copied.value = false), 1400)
  } catch {
    copied.value = false
  }
}
</script>

<template>
  <div class="code-tabs">
    <div class="tabs" role="tablist" :aria-label="label ?? '示例代码'">
      <button
        v-for="(item, i) in items"
        :key="item.id"
        class="tab"
        type="button"
        role="tab"
        :aria-selected="active === i"
        :class="{ on: active === i }"
        @click="active = i"
      >
        {{ item.label }}
      </button>
    </div>
    <div class="panel">
      <button class="copy" type="button" @click="copy(items[active].code)">
        {{ copied ? '已复制' : '复制' }}
      </button>
      <pre><code>{{ items[active].code }}</code></pre>
      <p v-if="items[active].note" class="note">{{ items[active].note }}</p>
    </div>
  </div>
</template>

<style scoped>
.code-tabs {
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  overflow: hidden;
  background: #fff;
}
.tabs {
  display: flex;
  gap: 2px;
  padding: 8px 8px 0;
  border-bottom: 1px solid var(--rb-line-soft);
  overflow-x: auto;
  scrollbar-width: none;
}
.tabs::-webkit-scrollbar {
  display: none;
}
.tab {
  flex: 0 0 auto;
  padding: 7px 13px;
  font: inherit;
  font-size: 13px;
  font-weight: 500;
  color: var(--rb-ink-3);
  background: none;
  border: 0;
  border-radius: 6px 6px 0 0;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  cursor: pointer;
  transition: color 0.16s, border-color 0.16s;
}
.tab:hover {
  color: var(--rb-ink);
}
.tab.on {
  color: var(--rb-ink);
  border-bottom-color: var(--rb-emerald);
}
.panel {
  position: relative;
  background: var(--rb-code-bg);
}
.panel pre {
  margin: 0;
  padding: 20px 22px;
  overflow-x: auto;
  font-family: var(--rb-font-mono);
  font-size: 13px;
  line-height: 1.72;
  color: #c9d3dd;
  tab-size: 2;
}
.panel code {
  font: inherit;
  white-space: pre;
}
.copy {
  position: absolute;
  top: 10px;
  right: 10px;
  padding: 4px 9px;
  font: inherit;
  font-size: 11px;
  color: #8b98a6;
  background: rgba(255, 255, 255, 0.06);
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 4px;
  cursor: pointer;
  transition: color 0.16s, border-color 0.16s;
}
.copy:hover {
  color: #e6edf3;
  border-color: rgba(255, 255, 255, 0.28);
}
.note {
  margin: 0;
  padding: 12px 22px 16px;
  font-size: 13px;
  line-height: 1.7;
  color: #8b98a6;
  border-top: 1px solid rgba(255, 255, 255, 0.08);
}
</style>
