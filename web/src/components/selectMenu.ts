/**
 * 自定义下拉（SelectMenu.vue）的选项类型。
 *
 * 单独放一个模块而不是写在 SFC 里：页面要在脚本里构造选项数组，
 * 从 .vue 里 import 类型对构建/编辑器都不稳定。
 */
export interface SelectMenuOption<T extends string | number = string> {
  /** 提交给 v-model 的值（字符串或数字；不要用对象，避免引用比较陷阱）。 */
  value: T
  /** 下拉里显示的主文案。 */
  label: string
  /** 可选的次要说明（小字，展示在 label 下方）。 */
  desc?: string
}
