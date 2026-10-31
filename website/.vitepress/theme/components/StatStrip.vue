<script setup lang="ts">
defineProps<{
  items: { value: string; label: string; note?: string }[]
}>()
</script>

<template>
  <ul class="stats">
    <li v-for="item in items" :key="item.label">
      <strong class="tnum">{{ item.value }}</strong>
      <span class="label">{{ item.label }}</span>
      <span v-if="item.note" class="note">{{ item.note }}</span>
    </li>
  </ul>
</template>

<style scoped>
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0;
  margin: 26px 0 8px;
  padding: 0;
  list-style: none;
  border-top: 1px solid var(--rb-line);
  border-bottom: 1px solid var(--rb-line);
}
.stats li {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 18px 18px 20px 0;
  border-right: 1px solid var(--rb-line-soft);
}
.stats li:last-child {
  border-right: 0;
}
.stats strong {
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--rb-ink);
}
.label {
  font-size: 13px;
  color: var(--rb-ink-2);
}
.note {
  font-size: 12px;
  line-height: 1.5;
  color: var(--rb-ink-3);
}
@media (max-width: 900px) {
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .stats li:nth-child(2) {
    border-right: 0;
  }
}
@media (max-width: 560px) {
  .stats {
    grid-template-columns: 1fr;
  }
  .stats li {
    border-right: 0;
    border-bottom: 1px solid var(--rb-line-soft);
    padding: 14px 0;
  }
  .stats li:last-child {
    border-bottom: 0;
  }
}
</style>
