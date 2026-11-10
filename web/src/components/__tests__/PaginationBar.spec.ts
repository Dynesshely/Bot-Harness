import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import PaginationBar from '@/components/PaginationBar.vue'

/**
 * 分页条：每页条数改成自定义下拉之后，emit 语义必须与原生 <select> 一致
 * （update:pageSize 是数字，不是事件对象里的字符串）。
 */
describe('PaginationBar', () => {
  it('每页条数是数字下拉，选中后 emit update:pageSize(number)', async () => {
    const wrapper = mount(PaginationBar, {
      props: { page: 1, pageSize: 20, total: 120, pageSizeOptions: [20, 50, 100] },
      attachTo: document.body,
    })
    expect(wrapper.text()).toContain('每页')
    expect(wrapper.find('[role="combobox"]').text()).toContain('20 条/页')

    await wrapper.find('[role="combobox"]').trigger('click')
    const option = wrapper.findAll('[role="option"]').find((item) => item.text().includes('50'))
    expect(option).toBeTruthy()
    await option?.trigger('click')

    expect(wrapper.emitted('update:pageSize')?.[0]).toEqual([50])
    wrapper.unmount()
  })

  it('翻页按钮在边界禁用，并 emit update:page', async () => {
    const wrapper = mount(PaginationBar, { props: { page: 1, pageSize: 20, total: 120 } })
    const prev = wrapper.find('button[aria-label="上一页"]')
    const next = wrapper.find('button[aria-label="下一页"]')
    expect(prev.attributes('disabled')).toBeDefined()
    await next.trigger('click')
    expect(wrapper.emitted('update:page')?.[0]).toEqual([2])
    // 共 6 页，第 1 页显示 1-20
    expect(wrapper.text()).toContain('1-20')
    wrapper.unmount()
  })

  it('最后一页的下一页按钮禁用', () => {
    const wrapper = mount(PaginationBar, { props: { page: 6, pageSize: 20, total: 120 } })
    const next = wrapper.find('button[aria-label="下一页"]')
    expect(next.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('6 / 6')
    wrapper.unmount()
  })
})
