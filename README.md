# kube-res-foresight

面向 Kubernetes 资源预测、Pod 弹性扩容与容量规划的平台，包含以下业务模块：

- 数据源与预测：Prometheus 数据源、指标映射与内置指标预设、算法与时序模型管理、模块（Kubernetes 工作负载，自带多指标来源与预测周期，选择不预测、固定或动态算法）、动态多约束副本数求解、评分排名、回测、推荐与实测评估、统一指标查询。
- 运行概览与效果：需要关注的模块、实际扩缩容活动、预测效果报告、Pod·小时与容量 SLO、只读变更预览。
- AI 助手与配置：右上角聊天、运行状态导出、内置与自定义 Skill、多 LLM 连接和默认模型、定时提问与邮件发送；查询分析不修改资源与副本，聊天可管理用户自己的 AI 定时任务。
- kube-res-foresight-operator：读平台的自动模块并写副本（替代 HPA/KEDA；没有 CRD，决策来源只有平台），并导出 kube_res_foresight_operator_* 指标。
- 系统管理：系统参数、登录日志、操作日志、API 凭据。
- 授权管理：用户、角色、菜单和 API 权限。
- 任务中心：任务定义、调度、执行记录和失败重试。

个人资料页是隐藏的登录自助页面。进度与未验证项见 docs/platform-progress.md。

## 项目官网

中英文官网与文档中心位于 [site/](site/README.md)，编辑源位于 `website/`。文档包含入门、核心概念、算法参数、使用指南、AI 助手和运维管理六组；新增功能可查阅 [AI 助手](site/docs/assistant.html)、[LLM 配置](site/docs/llm-configuration.html)、[定时任务与邮件](site/docs/ai-tasks.html)及[首页概览](site/docs/dashboard.html)。修改后生成页面，再启动预览：

```sh
python scripts/build-site.py
python -m http.server 4173 --bind 127.0.0.1 --directory site
```

访问 `http://127.0.0.1:4173`。GitHub Pages 工作流位于 `.github/workflows/pages.yml`，发布步骤见 [官网维护说明](site/README.md)。

## 自动构建与 Release

工作流位于 [release.yml](.github/workflows/release.yml)。推送 `v*` 标签后，自动编译管理前端并创建 GitHub Release；在 Actions 中手动运行 **kube-res-foresight release** 也能构建，选择分支运行时仅上传 Actions Artifacts，选择版本标签运行时发布对应 Release。

发布产物包括：

- `kube-res-foresight-web_<版本>.tar.gz`：独立管理前端静态文件。
- `kube-res-foresight_<版本>_<系统>_<架构>`：已嵌入管理控制台的平台，启动后直接访问，无需另外部署前端。
- `kube-res-foresight-operator_<版本>_<系统>_<架构>`：独立 Operator。
- `SHA256SUMS`：Release 资产校验和。系统为 Linux、macOS、Windows，架构为 amd64、arm64；Windows 使用 zip，其余使用 tar.gz。

前端源码从独立仓库 `cylonchau/kube-res-foresight-web` 拉取。默认使用 `main`，正式版本可在本仓库 **Settings → Secrets and variables → Actions → Variables** 中设置 `FRONTEND_REF` 为前端提交 SHA；手动运行的 `frontend_ref` 可以覆盖它。前端产物中的 `build-info.json` 记录前后端实际提交及版本。若前端仓库为私有，需要设置具有该仓库 Contents 读取权限的 Secret `FRONTEND_READ_TOKEN`。

```sh
git tag v0.1.0
git push origin v0.1.0
```

单独部署前端时，将压缩包解压到静态服务器根目录，配置 SPA history 回退到 `index.html`，并把 `/api/`（含 WebSocket）反向代理到平台。GitHub Pages 用于 `site/` 官网；管理控制台需要连接运行中的后端服务。

## 开发与启动

后端沿用 Go、Gin、GORM、Cobra、Casbin；管理界面沿用 Vue 3、Naive UI、Pinia、Elegant Router。

```sh
cd web
pnpm install --frozen-lockfile
pnpm build
cd ..
make package-web
go build -o target/bin/kube-res-foresight ./cmd
target/bin/kube-res-foresight server --config config.yaml --migration
target/bin/kube-res-foresight all --config config.yaml
```

也可以分别启动 `kube-res-foresight server` 和 `kube-res-foresight job`。配置与数据库驱动沿用原有方式。

配置环境变量统一使用 `KUBE_RES_FORESIGHT_` 前缀，凭据加密密钥使用 `KUBE_RES_FORESIGHT_SECRET_KEY`。
API 客户端来源与开发者凭据请求头使用 `X-kube-res-foresight-Client`、`X-kube-res-foresight-AccessKey` 和 `X-kube-res-foresight-AccessSecret`。
已有安装应保持数据库路径和密钥值不变，仅更新配置变量名称；品牌更名不迁移或重置运行数据库。
环境变量兼容读取、API 客户端及 Kubernetes 对象切换说明见 [名称与升级说明](docs/renaming.md)。

## 数据库升级

已有安装请先备份数据库，再执行 `kube-res-foresight server --config config.yaml --upgrade`。
该显式升级会移除已退役模块的表、菜单、任务和设置；保留用户、角色、权限、系统设置、自定义通用任务，以及登录与操作日志。升级会先打印将被删除的表及其行数，再执行删除。普通启动不删表。
迁移内的历史表名仅用于升级清理，不代表仍支持那些功能。

## 验证

```sh
go test ./...
cd web
pnpm typecheck
pnpm test:unit
pnpm build
```

构建产物放在 `target/bin`；运行数据库、日志、凭据和依赖缓存不入 Git。
