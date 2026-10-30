import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './styles/v2rayn-like.css'
import './styles/theme.css'

// Note: the native WebView2 context menu is blocked by the
// CustomContextMenu component (capture-phase listener, no exceptions),
// which shows Cut/Copy/Paste/Select All inside editable fields.

const app = createApp(App)
app.use(createPinia())
app.mount('#app')

// TEMP perf harness (removed after measuring):
// /?perfseed=3000 fills the profiles store with synthetic rows.
if (import.meta.env.DEV && new URLSearchParams(location.search).has('perfseed')) {
  const n = Number(new URLSearchParams(location.search).get('perfseed')) || 3000
  window.setTimeout(async () => {
    const { useProfilesStore } = await import('./stores/profiles')
    const store = useProfilesStore()
    const protos = ['vless', 'vmess', 'trojan', 'shadowsocks']
    const nets = ['tcp', 'ws', 'grpc']
    const cc = ['US', 'DE', 'JP', 'SG', '']
    store.profiles = Array.from({ length: n }, (_, i) => ({
      id: `seed-${i}`,
      name: `Server ${i}`,
      core: 0,
      protocol: protos[i % protos.length],
      address: `10.0.${i % 256}.${(i * 7) % 256}`,
      port: 443,
      network: nets[i % nets.length],
      group: i % 5 === 0 ? 'Feed A' : '',
      countryCode: cc[i % cc.length],
      lastLatency: i % 3 === 0 ? 40 + (i % 200) : 0,
    })) as never
  }, 2500)
}
