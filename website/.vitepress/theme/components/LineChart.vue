<script setup lang="ts">
import { computed } from 'vue'
import EChart from './EChart.vue'
import { AXIS_COLOR, INK, LABEL_COLOR } from '../echarts'

const props = withDefaults(
  defineProps<{
    points: { label: string; value: number }[]
    unit?: string
    height?: number
    note?: string
    labels?: boolean
  }>(),
  { unit: '', height: 260, note: '', labels: true }
)

const description = computed(() =>
  props.points.map((point) => `${point.label} ${point.value}${props.unit}`).join('，')
)

const option = computed(() => ({
  grid: { left: 4, right: 26, top: props.labels ? 40 : 20, bottom: 4, containLabel: true },
  xAxis: {
    type: 'category',
    data: props.points.map((point) => point.label),
    boundaryGap: false,
    axisLine: { lineStyle: { color: AXIS_COLOR } },
    axisTick: { show: false },
    axisLabel: { color: LABEL_COLOR, fontSize: 12, margin: 14 }
  },
  yAxis: {
    type: 'value',
    splitLine: { lineStyle: { color: 'rgba(0, 0, 0, 0.06)' } },
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: { color: LABEL_COLOR, fontSize: 11 }
  },
  tooltip: {
    trigger: 'axis',
    axisPointer: { type: 'line', lineStyle: { color: 'rgba(4, 120, 87, 0.35)' } },
    valueFormatter: (value: number | string) => `${value}${props.unit}`
  },
  series: [
    {
      type: 'line',
      smooth: 0.35,
      symbol: 'circle',
      symbolSize: 8,
      itemStyle: { color: '#047857', borderColor: '#ffffff', borderWidth: 2 },
      lineStyle: { width: 2.5, color: '#047857' },
      areaStyle: {
        color: {
          type: 'linear',
          x: 0,
          y: 0,
          x2: 0,
          y2: 1,
          colorStops: [
            { offset: 0, color: 'rgba(4, 120, 87, 0.20)' },
            { offset: 1, color: 'rgba(4, 120, 87, 0)' }
          ]
        }
      },
      label: {
        show: props.labels,
        position: 'top',
        distance: 10,
        color: INK,
        fontSize: 12,
        fontWeight: 600,
        formatter: (params: { value: number }) => `${params.value}${props.unit}`
      },
      emphasis: { focus: 'series' },
      data: props.points.map((point) => point.value)
    }
  ]
}))
</script>

<template>
  <figure class="chart">
    <EChart :option="option" :height="height" :label="description" />
    <figcaption v-if="note">{{ note }}</figcaption>
  </figure>
</template>

<style scoped>
.chart {
  margin: 20px 0 4px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
  padding: 8px 10px 4px;
}
figcaption {
  padding: 2px 6px 10px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--rb-ink-3);
}
</style>
