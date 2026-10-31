import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import Home from './Home.vue'
import LineChart from './components/LineChart.vue'
import MeterBars from './components/MeterBars.vue'
import StatStrip from './components/StatStrip.vue'
import { vReveal } from './motion/reveal'
import './style.css'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('Home', Home)
    app.component('LineChart', LineChart)
    app.component('MeterBars', MeterBars)
    app.component('StatStrip', StatStrip)
    app.directive('reveal', vReveal)
  }
} satisfies Theme
