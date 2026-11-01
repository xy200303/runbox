<script setup lang="ts">
import { computed } from 'vue'
import EChart from './EChart.vue'
import { INK, LABEL_COLOR } from '../echarts'

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
  return Math.max(...props.items.map((item) => item.value), 1) * 1.04
})

const labels = computed(() => props.items.map((item) => item.label))
const displays = computed(() =>
  props.items.map((item) => item.display ?? `${item.value} ${props.unit}`.trim())
)

const option = computed(() => ({
  grid: { left: 4, right: 104, top: 6, bottom: 6, containLabel: true },
  xAxis: { type: 'value', max: peak.value, show: false },
  yAxis: {
    type: 'category',
    data: labels.value,
    inverse: true,
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: { color: LABEL_COLOR, fontSize: 12, margin: 12 }
  },
  tooltip: {
    trigger: 'item',
    formatter: (params: { dataIndex: number }) =>
      `${labels.value[params.dataIndex]}<br/><b>${displays.value[params.dataIndex]}</b>`
  },
  series: [
    {
      type: 'bar',
      barWidth: 9,
      showBackground: true,
      backgroundStyle: { color: 'rgba(0, 0, 0, 0.05)', borderRadius: 5 },
      itemStyle: {
        borderRadius: 5,
        color: {
          type: 'linear',
          x: 0,
          y: 0,
          x2: 1,
          y2: 0,
          colorStops: [
            { offset: 0, color: '#047857' },
            { offset: 1, color: '#22b07d' }
          ]
        }
      },
      label: {
        show: true,
        position: 'right',
        distance: 10,
        color: INK,
        fontSize: 12,
        fontFamily: "'JetBrains Mono', monospace",
        formatter: (params: { dataIndex: number }) => displays.value[params.dataIndex]
      },
      data: props.items.map((item) => item.value)
    }
  ]
}))

const rows = computed(() => props.items.length)
</script>

<template>
  <div class="meter">
    <EChart :option="option" :height="rows * 42 + 16" :label="displays.join('，')" />
    <p v-if="note" class="note">{{ note }}</p>
  </div>
</template>

<style scoped>
.meter {
  margin: 20px 0 4px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
  padding: 14px 14px 6px;
}
.note {
  margin: 6px 4px 8px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--rb-ink-3);
}
</style>
