# 前端工程约束

项目管理前端在根目录 web。保持现有 Vue/Naive UI 框架、搜索卡片和数据内容布局，品牌使用 kube-resource-fcst。参考最近页面，优先公共组件，禁止每页独立设计。

## 列表与分页

- 标准列表结构：搜索卡片 → 表头操作 → ExpandableDataTable + 分页 → 操作列；检索排序走服务端。
- 分页包含自适应当前视口行数及 30/50/100/200；列设置、可见列和宽度按用户入库，复用 use-table-column-config。
- 必要列支持点击复制；展开箭头、复选框、批量操作和菜单图标沿用公共实现。
- 列标题短，长解释放问号提示；同类列宽一致，一个事实一列。操作横排，NSpace wrap=false，明确 width/minWidth，按钮不竖排。
- 状态用标签，启停放单行及批量操作，不能拿开关直接充状态。
- flex-height 表格父容器必须有明确高度，只有 min-height 可能导致零行；检查不同分辨率及最小高度。

## 搜索、表单和展示

- 中英文 i18n 完整，不残留变量/翻译键。placeholder 指明字段，使用 common.inputField/common.selectField，不用空泛的“请输入/请选择/全部”。
- 搜索复用 web/src/plugins/list-search.ts：NForm class="list-search-form" :model="search"，保留重置/搜索按钮，公共模块 0.3 秒防抖触发。页面不要再写一套自动提交。
- 提示和敏感字段说明放字段旁 ?（IconTooltip）或表头小标签，避免常驻警告横幅/说明卡占布局；实际错误仍需清楚可见。
- 新增/编辑标签在上方（label-placement=top），字段与说明紧凑。多字段使用 edit-modal-wide + NGrid，自适应窄屏；算法参数遵循专属宽屏 schema 编辑要求。
- 功能分区优先清晰菜单，不把互不相关业务堆在单页标签中；列表数据分页仍必须保留。
- 图表分区独立且不重叠，在最小高度检查；弹窗图表测试打开→关闭→再打开的资源释放。

按改动选择 typecheck、单元测试、构建或页面验证；前后端接口同步，分别检查 web 与根仓库状态，按功能整理交付。
