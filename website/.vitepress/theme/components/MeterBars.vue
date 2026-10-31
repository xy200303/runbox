<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    items: { label: string; value: number; display?: string }[]
    unit?: string
    max?: number
    note?: string
  }>(),
  { unit: '', max: 0, note: '' }
)

const peak = computed(() => {
  if (props.max > 0) return props.max
  const top = Math.max(...props.items.map((item) => item.value), 1)
  return top * 1.06
})

const width = (value: number) => `${Math.max(1.5, (value / peak.value) * 100)}%`
</script>

<template>
  <div class="meter" role="img" :aria-label="items.map((i) => `${i.label}: ${i.display ?? i.value + ' ' + unit}`).join('; ')">
    <div v-for="item in items" :key="item.label" class="row">
      <span class="label">{{ item.label }}</span>
      <span class="track"><span class="fill" :style="{ width: width(item.value) }" /></span>
      <span class="value tnum">{{ item.display ?? `${item.value} ${unit}` }}</span>
    </div>
    <p v-if="note" class="note">{{ note }}</p>
  </div>
</template>

<style scoped>
.meter {
  margin: 20px 0 4px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
  padding: 18px 20px 10px;
}
.row {
  display: grid;
  grid-template-columns: 148px minmax(0, 1fr) 92px;
  align-items: center;
  gap: 14px;
  padding: 7px 0;
}
.label {
  font-size: 13px;
  color: var(--rb-ink-2);
}
.track {
  display: block;
  height: 10px;
  border-radius: 3px;
  background: rgba(0, 0, 0, 0.055);
  overflow: hidden;
}
.fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: linear-gradient(90deg, var(--rb-emerald-600), #0ea66f);
  transition: width 0.6s cubic-bezier(0.16, 1, 0.3, 1);
}
.value {
  font-family: var(--rb-font-mono);
  font-size: 12.5px;
  color: var(--rb-ink);
  text-align: right;
}
.note {
  margin: 10px 0 6px;
  font-size: 12.5px;
  color: var(--rb-ink-3);
}
@media (max-width: 640px) {
  .row {
    grid-template-columns: 96px minmax(0, 1fr) 76px;
    gap: 10px;
  }
  .label {
    font-size: 12px;
  }
}
</style>
