import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import SelectMenu from '@/components/SelectMenu.vue'
import type { SelectMenuOption } from '@/components/selectMenu'

/**
 * 自定义下拉：键盘 / 点击外部 / 禁用 / 空选项。
 *
 * 这些都是原生 <select> 免费提供、换成自己画之后必须自己保证的行为 ——
 * 回归一次就是「键盘用户打不开下拉」或「点一下旁边就关不掉」。
 */

const OPTIONS: SelectMenuOption<string>[] = [
  { value: 'join', label: '插话', desc: '值得接一句' },
  { value: 'wait', label: '等一等' },
  { value: 'skip', label: '不插话' },
]

function mountMenu(props: Record<string, unknown> = {}) {
  return mount(SelectMenu, {
    props: { modelValue: 'join', options: OPTIONS, label: '判定', ...props },
    attachTo: document.body,
  })
}

function triggerOf(wrapper: ReturnType<typeof mountMenu>) {
  return wrapper.find('[role="combobox"]')
}

describe('SelectMenu 展示', () => {
  it('渲染 label、选中项文案与 aria 属性，未展开时没有 listbox', () => {
    const wrapper = mountMenu()
    expect(wrapper.text()).toContain('判定')
    expect(triggerOf(wrapper).text()).toContain('插话')
    const trigger = triggerOf(wrapper)
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(trigger.attributes('aria-haspopup')).toBe('listbox')
    expect(trigger.attributes('aria-controls')).toBeTruthy()
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('没有选中值时显示 placeholder（灰色）', () => {
    const wrapper = mountMenu({ modelValue: '' , placeholder: '请选择' })
    expect(triggerOf(wrapper).text()).toContain('请选择')
    expect(triggerOf(wrapper).find('span').classes()).toContain('text-slate-500')
    wrapper.unmount()
  })

  it('数字 value 也能正确匹配（每页条数这类选项）', () => {
    const wrapper = mount(SelectMenu, {
      props: {
        modelValue: 50,
        options: [
          { value: 20, label: '20 条/页' },
          { value: 50, label: '50 条/页' },
        ],
      },
      attachTo: document.body,
    })
    expect(triggerOf(wrapper).text()).toContain('50 条/页')
    wrapper.unmount()
  })
})

describe('SelectMenu 键盘', () => {
  it('ArrowDown / ArrowUp 打开并移动高亮，Enter 选择后发 update:modelValue', async () => {
    const wrapper = mountMenu({ modelValue: 'join' })
    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowDown' })
    expect(triggerOf(wrapper).attributes('aria-expanded')).toBe('true')
    // 打开时高亮停在已选项（join）上
    expect(wrapper.find('[role="option"]').classes()).toContain('bg-sky-500/15')

    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowDown' })
    // 第二项（wait）被高亮，并通过 aria-activedescendant 暴露给读屏
    const active = triggerOf(wrapper).attributes('aria-activedescendant')
    expect(active).toBeTruthy()
    expect(wrapper.find('#' + active).text()).toContain('等一等')

    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowUp' })
    expect(wrapper.find('#' + triggerOf(wrapper).attributes('aria-activedescendant')).text()).toContain(
      '插话',
    )

    await triggerOf(wrapper).trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['join'])
    // 选完关闭
    expect(triggerOf(wrapper).attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })

  it('ArrowUp 从顶部回绕到最后一项；Home / End 到首尾', async () => {
    const wrapper = mountMenu({ modelValue: 'join' })
    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowDown' })
    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowUp' })
    const active = triggerOf(wrapper).attributes('aria-activedescendant')
    expect(wrapper.find('#' + active).text()).toContain('不插话')

    await triggerOf(wrapper).trigger('keydown', { key: 'Home' })
    expect(wrapper.find('#' + triggerOf(wrapper).attributes('aria-activedescendant')).text()).toContain(
      '插话',
    )
    await triggerOf(wrapper).trigger('keydown', { key: 'End' })
    expect(wrapper.find('#' + triggerOf(wrapper).attributes('aria-activedescendant')).text()).toContain(
      '不插话',
    )
    wrapper.unmount()
  })

  it('Escape 关闭且不改值；关闭后按 Escape 不报错', async () => {
    const wrapper = mountMenu()
    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowDown' })
    await triggerOf(wrapper).trigger('keydown', { key: 'Escape' })
    expect(triggerOf(wrapper).attributes('aria-expanded')).toBe('false')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    // 已关闭时再按一次 Escape：什么也不做（不是抛错）
    await triggerOf(wrapper).trigger('keydown', { key: 'Escape' })
    expect(triggerOf(wrapper).attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })

  it('展开时按 Escape（焦点在别处）同样关闭', async () => {
    const wrapper = mountMenu()
    await triggerOf(wrapper).trigger('click')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('SelectMenu 鼠标', () => {
  it('点击触发按钮展开，点击选项发事件并关闭', async () => {
    const wrapper = mountMenu()
    await triggerOf(wrapper).trigger('click')
    expect(wrapper.findAll('[role="option"]')).toHaveLength(3)
    await wrapper.findAll('[role="option"]')[2]?.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['skip'])
    expect(wrapper.emitted('change')?.[0]).toEqual(['skip'])
    expect(triggerOf(wrapper).attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })

  it('选中当前项不算 change（避免父组件白跑一次请求）', async () => {
    const wrapper = mountMenu({ modelValue: 'join' })
    await triggerOf(wrapper).trigger('click')
    await wrapper.findAll('[role="option"]')[0]?.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['join'])
    expect(wrapper.emitted('change')).toBeUndefined()
    wrapper.unmount()
  })

  it('点击外部关闭（pointerdown）', async () => {
    const wrapper = mountMenu()
    await triggerOf(wrapper).trigger('click')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('点击下拉内部不会关闭', async () => {
    const wrapper = mountMenu()
    await triggerOf(wrapper).trigger('click')
    const list = wrapper.find('[role="listbox"]')
    list.element.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('SelectMenu 多实例', () => {
  it('打开第二个下拉时，第一个自动收起（同屏不叠弹层）', async () => {
    const first = mountMenu()
    const second = mountMenu({ modelValue: 'wait' })
    await triggerOf(first).trigger('click')
    expect(first.find('[role="listbox"]').exists()).toBe(true)
    await triggerOf(second).trigger('click')
    await first.vm.$nextTick()
    expect(second.find('[role="listbox"]').exists()).toBe(true)
    expect(first.find('[role="listbox"]').exists()).toBe(false)
    first.unmount()
    second.unmount()
  })
})

describe('SelectMenu 禁用与空选项', () => {
  it('disabled：按钮禁用、点击 / 键盘都不展开', async () => {
    const wrapper = mountMenu({ disabled: true })
    expect(triggerOf(wrapper).attributes('disabled')).toBeDefined()
    await triggerOf(wrapper).trigger('click')
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    await triggerOf(wrapper).trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })

  it('空选项：可以展开，显示占位提示，Enter 不会发事件', async () => {
    const wrapper = mountMenu({ options: [], modelValue: '' })
    await triggerOf(wrapper).trigger('click')
    const list = wrapper.find('[role="listbox"]')
    expect(list.exists()).toBe(true)
    expect(list.text()).toContain('无可选项')
    expect(wrapper.findAll('[role="option"]')).toHaveLength(0)
    await triggerOf(wrapper).trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })
})
