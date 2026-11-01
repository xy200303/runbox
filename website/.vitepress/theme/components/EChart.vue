<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { baseOption, loadECharts, type EChartsInstance } from '../echarts'

const props = withDefaults(
  defineProps<{
    option: Record<string, unknown>
    height?: number
    label?: string
  }>(),
  { height: 240, label: 'chart' }
)

const host = ref<HTMLDivElement | null>(null)
let instance: EChartsInstance | null = null
let observer: ResizeObserver | null = null
let disposed = false

const merged = () => ({ ...baseOption(), ...props.option })

async function render() {
  if (!host.value) return
  const core = await loadECharts()
  if (disposed || !host.value) return
  if (!instance) {
    instance = core.init(host.value, undefined, { renderer: 'canvas' })
    observer = new ResizeObserver(() => instance?.resize())
    observer.observe(host.value)
  }
  instance.setOption(merged(), true)
}

onMounted(render)

watch(
  () => props.option,
  () => {
    if (instance) instance.setOption(merged(), true)
  },
  { deep: true }
)

onBeforeUnmount(() => {
  disposed = true
  observer?.disconnect()
  observer = null
  instance?.dispose()
  instance = null
})
</script>

<template>
  <div
    ref="host"
    class="echart"
    :style="{ height: `${height}px` }"
    role="img"
    :aria-label="label"
  />
</template>

<style scoped>
.echart {
  width: 100%;
  min-width: 0;
}
</style>
