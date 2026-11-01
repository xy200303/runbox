/* ===========================================================================
   ECharts loader
   - Only the pieces the site uses are imported, and only in the browser: the
     module is pulled in when the first chart mounts, so documentation pages
     never pay for the chart bundle.
   - Registered once; later charts reuse the same module instance.
   =========================================================================== */

type EChartsModule = typeof import('echarts/core')
type EChartsInstance = import('echarts/core').EChartsType

let loader: Promise<EChartsModule> | null = null

export function loadECharts(): Promise<EChartsModule> {
  if (!loader) {
    loader = (async () => {
      const [core, charts, components, renderers] = await Promise.all([
        import('echarts/core'),
        import('echarts/charts'),
        import('echarts/components'),
        import('echarts/renderers')
      ])
      core.use([
        charts.LineChart,
        charts.BarChart,
        components.GridComponent,
        components.TooltipComponent,
        components.AxisPointerComponent,
        renderers.CanvasRenderer
      ])
      return core
    })()
  }
  return loader
}

export const PALETTE = ['#047857', '#0ea66f', '#34d399']

export const AXIS_COLOR = 'rgba(0, 0, 0, 0.10)'
export const LABEL_COLOR = '#8b8f96'
export const INK = '#16181c'

/** Shared typography and tooltip styling for every chart on the site. */
export function baseOption() {
  return {
    animationDuration: 700,
    animationEasing: 'cubicOut' as const,
    textStyle: {
      fontFamily:
        "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif"
    },
    tooltip: {
      backgroundColor: '#0d1117',
      borderWidth: 0,
      padding: [8, 12],
      textStyle: { color: '#e6edf3', fontSize: 12 },
      extraCssText: 'border-radius: 6px; box-shadow: 0 8px 24px -12px rgba(0,0,0,.45);'
    }
  }
}

export type { EChartsInstance }
