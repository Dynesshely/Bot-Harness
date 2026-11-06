import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { setUnauthorizedHandler, toMessage } from '@/api'
import { resetAuth } from '@/stores/auth'
import { toast } from '@/composables/useToast'
import './style.css'

// 统一的 401 处理：清空登录态并跳转登录页（toast 只提示一次）
setUnauthorizedHandler(() => {
  resetAuth()
  const current = router.currentRoute.value
  if (current.name === 'login') return
  toast.warn('登录状态已失效，请重新登录')
  void router.replace({
    name: 'login',
    query: current.fullPath && current.fullPath !== '/' ? { redirect: current.fullPath } : {},
  })
})

const app = createApp(App)

app.config.errorHandler = (error) => {
  console.error(error)
  toast.error(toMessage(error))
}

app.use(router)
app.mount('#app')
