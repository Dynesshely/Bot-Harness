import { getCurrentInstance, onBeforeUnmount, onMounted, type Ref } from 'vue'
import { onBeforeRouteLeave, useRoute } from 'vue-router'

/**
 * 有未保存修改时，关闭/刷新页面与路由离开都要二次确认。
 * 只读 dirty，不修改页面状态；调用方负责提供真正的 dirty 计算。
 */
export function useUnsavedGuard(dirty: Ref<boolean>) {
  function beforeUnload(event: BeforeUnloadEvent) {
    if (!dirty.value) return
    event.preventDefault()
    // 现代浏览器忽略自定义文案，但必须给 returnValue 才会弹确认。
    event.returnValue = ''
  }

  onMounted(() => window.addEventListener('beforeunload', beforeUnload))
  onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

  // 单元测试直接挂载页面时没有 router；此时只保留 beforeunload。
  const route = getCurrentInstance()?.appContext.config.globalProperties.$router ? useRoute() : null
  if (import.meta.env.MODE !== 'test' && route && route.name) {
    onBeforeRouteLeave(() => {
      if (!dirty.value) return true
      return window.confirm('有未保存的修改，确定离开吗？')
    })
  }
}
