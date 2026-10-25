# kube-resource-fcst 官网

官网源码位于 `site/`，独立于管理前端 `web/`；平台部署与管理操作请阅读使用指南。

- `index.html`：项目介绍、预测流程示意、可切换功能架构说明、渐进上线与文档入口。
- `docs.html`：11 个中文章节，覆盖使用流程、功能架构、工作负载管理、数据、算法管理、回测、执行、安全、平台管理和使用边界。
- `en/index.html`、`en/docs.html`：英文首页与完整使用指南，与中文版保持对应章节和锚点。
- `assets/locale.js`：按浏览器语言选择中文或英文；导航栏支持切换，记住个人偏好并保留当前章节。
- `assets/adaptive.css`、`assets/light.css`、`assets/theme.js`：默认跟随浏览器，右上角外观按钮可选择“跟随系统 / 浅色 / 深色”，选择会跨页面保存；无脚本时仍按浏览器主题显示。
- `assets/config.js`：GitHub 源码仓库与分支配置。默认沿用当前 origin 的公开 URL；迁移仓库后在这里修改。HTML 中的 GitHub 链接是禁用脚本时的回退，也应同步修改。
- `assets/style.css`、`assets/main.js`：响应式样式与原生交互，无第三方前端运行依赖，无远程字体、追踪或访问凭据。
- `404.html`：Pages 缺失页面提示。

## 本地预览

从仓库根目录执行：

```sh
python -m http.server 4173 --bind 127.0.0.1 --directory site
```

打开 `http://127.0.0.1:4173`。预览项目站点前缀时，也可从仓库根目录启动服务器，再访问 `/site/`。

## 校验

```sh
python scripts/check-site.py
node --check site/assets/config.js
node --check site/assets/locale.js
node --check site/assets/theme.js
node --check site/assets/main.js
```

Python 校验会检查站内文件、锚点、重复 ID、交互目标、相对链接、语言标记、对应章节与内置算法数量。

## GitHub Pages 发布

1. 审查官网内容，确认 `assets/config.js` 的仓库地址与分支；更新功能时同步维护中英文说明。
2. 将 `site/`、`scripts/check-site.py` 和 `.github/workflows/pages.yml` 提交到目标仓库的默认分支。
3. 仓库 **Settings → Pages → Build and deployment → Source** 选择 **GitHub Actions**。仓库需具备 Pages 使用资格，Actions 需允许引用的官方 actions；环境保护规则需允许默认分支部署。
4. 在 **Actions → kube-resource-fcst website** 中手动运行工作流，或通过默认分支中相关文件的 push 触发。
5. `check` 完成校验后，`deploy` 上传 `site/` 并发布。访问部署的 `github-pages` 环境给出的 URL。PR 和其他分支只校验，不发布。

工作流使用 `configure-pages`、`upload-pages-artifact` 与 `deploy-pages`，不需要手写 GitHub Token。Pages 写权限和 OIDC 权限仅授予部署任务。官方流程：
https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages

站内链接与资源均为相对路径，适用于 `https://<owner>.github.io/<repo>/` 和用户站点根路径。没有 SPA 路由或构建步骤。`404.html` 的返回链接由发布任务按 Pages 的真实 base URL 写入，因此嵌套错误路径也能返回首页；直接本地预览 404 时使用相对链接。

## 内容维护

官网围绕功能架构、使用流程、管理场景与安全边界组织内容。维护中英文页面时同步更新标题、正文、导航、搜索提示、图表说明与交互文案。首页曲线标注为示例数据。部署命令随 README、Makefile 与工具链一起维护。

自动语言选择优先使用 URL 中的 `lang=zh/en`，其次使用已保存偏好，再参考浏览器首选语言；中文语言变体使用中文，其余语言使用英文。英文独立页面在未保存偏好时保留英文。禁用存储时，站内链接仍通过语言参数维持选择。禁用 JavaScript 时可直接访问对应语言的页面。
