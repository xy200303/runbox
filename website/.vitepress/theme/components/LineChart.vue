<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    points: { label: string; value: number }[]
    unit?: string
    height?: number
    note?: string
  }>(),
  { unit: '', height: 230, note: '' }
)

const W = 720
const pad = { left: 58, right: 18, top: 32, bottom: 34 }

// Round the axis up to a 1/2/2.5/5 x 10^n step so ticks stay integers.
const maxValue = computed(() => {
  const top = Math.max(...props.points.map((point) => point.value), 1)
  const rough = top * 1.15
  const magnitude = Math.pow(10, Math.floor(Math.log10(rough)))
  const normalized = rough / magnitude
  // Prefer steps that divide into four or five even ticks.
  const step = [1, 1.25, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find((candidate) => candidate >= normalized) ?? 10
  return step * magnitude
})

const innerWidth = W - pad.left - pad.right
const innerHeight = computed(() => props.height - pad.top - pad.bottom)

const xAt = (index: number) =>
  pad.left + (props.points.length === 1 ? innerWidth / 2 : (index * innerWidth) / (props.points.length - 1))
const yAt = (value: number) =>
  pad.top + (1 - value / maxValue.value) * innerHeight.value

const line = computed(() =>
  props.points.map((point, index) => `${index === 0 ? 'M' : 'L'}${xAt(index).toFixed(1)},${yAt(point.value).toFixed(1)}`).join(' ')
)

const area = computed(
  () =>
    `${line.value} L${xAt(props.points.length - 1).toFixed(1)},${(pad.top + innerHeight.value).toFixed(1)} ` +
    `L${xAt(0).toFixed(1)},${(pad.top + innerHeight.value).toFixed(1)} Z`
)

const grid = computed(() =>
  [0, 0.25, 0.5, 0.75, 1].map((fraction) => ({
    value: maxValue.value * fraction,
    y: yAt(maxValue.value * fraction)
  }))
)

const format = (value: number) => {
  if (value >= 1000) return `${Math.round(value / 100) / 10}k`
  return Number.isInteger(value) ? String(value) : value.toFixed(1)
}
</script>

<template>
  <figure class="chart">
    <svg :viewBox="`0 0 ${W} ${height}`" role="img"
         :aria-label="points.map((p) => `${p.label}: ${p.value} ${unit}`).join('; ')">
      <g class="grid">
        <line v-for="tick in grid" :key="tick.y" :x1="pad.left" :x2="W - pad.right"
              :y1="tick.y" :y2="tick.y" />
        <text v-for="tick in grid" :key="`t-${tick.y}`" :x="pad.left - 10" :y="tick.y + 4"
              text-anchor="end" class="axis">{{ format(tick.value) }}</text>
      </g>

      <path :d="area" class="area" />
      <path :d="line" class="line" />

      <g v-for="(point, index) in points" :key="point.label">
        <circle :cx="xAt(index)" :cy="yAt(point.value)" r="4.5" class="dot" />
        <text :x="xAt(index)" :y="yAt(point.value) - 12"
              :text-anchor="index === 0 ? 'start' : index === points.length - 1 ? 'end' : 'middle'"
              class="point-label">
          {{ point.value }}{{ unit }}
        </text>
        <text :x="xAt(index)" :y="height - 12" text-anchor="middle" class="axis">{{ point.label }}</text>
      </g>
    </svg>
    <figcaption v-if="note">{{ note }}</figcaption>
  </figure>
</template>

<style scoped>
.chart {
  margin: 20px 0 4px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
  padding: 10px 12px 6px;
}
svg {
  display: block;
  width: 100%;
  height: auto;
}
.grid line {
  stroke: rgba(0, 0, 0, 0.06);
  stroke-width: 1;
}
.axis {
  fill: var(--rb-ink-3);
  font-family: var(--rb-font-mono);
  font-size: 11px;
}
.area {
  fill: rgba(4, 120, 87, 0.08);
}
.line {
  fill: none;
  stroke: var(--rb-emerald-600);
  stroke-width: 2;
  stroke-linejoin: round;
  stroke-linecap: round;
}
.dot {
  fill: #fff;
  stroke: var(--rb-emerald-600);
  stroke-width: 2.5;
}
.point-label {
  fill: var(--rb-ink);
  font-family: var(--rb-font-mono);
  font-size: 12px;
  font-weight: 600;
}
figcaption {
  padding: 2px 4px 8px;
  font-size: 12.5px;
  color: var(--rb-ink-3);
}
</style>
