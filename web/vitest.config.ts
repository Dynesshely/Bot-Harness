import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

/**
 * 管理后台的单测配置。
 *
 * 与 `vite.config.ts` 刻意分开：构建用的 dev server / API 代理 / Tailwind 插件在单测里
 * 全是噪音，这里只保留「能编译 .vue + 能解析 @ 别名」这两件必需的事。
 * 环境固定 jsdom：路由用 createWebHistory，组件要挂到 document 上。
 */
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
    setupFiles: ['src/test/setup.ts'],
    // 每个用例自己控制 mock 生命周期，禁止残留的 spy 影响下一个用例。
    restoreMocks: true,
    clearMocks: true,
  },
})
