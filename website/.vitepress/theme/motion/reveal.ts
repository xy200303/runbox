/* ===========================================================================
   v-reveal · 滚动入场
   元素在视口下方时先隐藏，进入视口后交给 CSS transition 淡入上移。
   首屏元素不加隐藏态（IntersectionObserver 首帧就会判定为可见，不会闪）。
   尊重 prefers-reduced-motion：关掉动效时页面必须依然完整可读。
   =========================================================================== */
import type { Directive } from 'vue'

const teardowns = new WeakMap<HTMLElement, () => void>()

const reduced = () =>
  typeof window !== 'undefined' &&
  window.matchMedia('(prefers-reduced-motion: reduce)').matches

export const vReveal: Directive<HTMLElement, number | undefined> = {
  mounted(el, binding) {
    if (reduced() || typeof IntersectionObserver === 'undefined') return
    const delay = typeof binding.value === 'number' ? binding.value : 0
    el.style.transitionDelay = `${delay}ms`
    el.classList.add('reveal-pre')
    const io = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue
          el.classList.remove('reveal-pre')
          el.removeAttribute('style')
          io.disconnect()
        }
      },
      { rootMargin: '0px 0px -10% 0px', threshold: 0.01 }
    )
    io.observe(el)
    teardowns.set(el, () => io.disconnect())
  },
  unmounted(el) {
    teardowns.get(el)?.()
    teardowns.delete(el)
  }
}
