const navToggle = document.querySelector('#nav-toggle')
const navLinks = document.querySelector('#nav-links')

navToggle?.addEventListener('click', () => {
  const open = navLinks?.classList.toggle('open')
  navToggle.setAttribute('aria-expanded', String(Boolean(open)))
})

for (const link of document.querySelectorAll('.nav-links a')) {
  link.addEventListener('click', () => {
    navLinks?.classList.remove('open')
    navToggle?.setAttribute('aria-expanded', 'false')
  })
}

const tabs = document.querySelectorAll('.tab')
const panels = document.querySelectorAll('.tab-panel')

for (const tab of tabs) {
  tab.addEventListener('click', () => {
    const target = tab.dataset.tab
    for (const item of tabs) item.classList.toggle('active', item === tab)
    for (const panel of panels) panel.classList.toggle('active', panel.dataset.panel === target)
  })
}

for (const block of document.querySelectorAll('pre')) {
  const button = document.createElement('button')
  button.type = 'button'
  button.className = 'copy'
  button.textContent = '复制'
  button.addEventListener('click', async () => {
    const text = block.querySelector('code')?.textContent ?? block.textContent ?? ''
    try {
      await navigator.clipboard.writeText(text)
      button.textContent = '已复制'
    } catch {
      button.textContent = '复制失败'
    }
    setTimeout(() => {
      button.textContent = '复制'
    }, 1600)
  })
  block.append(button)
}

const reveals = document.querySelectorAll('.feature, .panel, .sdk, .arch-node, .compare-wrap, .faq details')
for (const element of reveals) element.classList.add('reveal')

const observer = new IntersectionObserver(
  (entries) => {
    for (const entry of entries) {
      if (entry.isIntersecting) {
        entry.target.classList.add('visible')
        observer.unobserve(entry.target)
      }
    }
  },
  { threshold: 0.12 },
)

for (const element of reveals) observer.observe(element)
