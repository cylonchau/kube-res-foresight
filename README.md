# kube-resource-fcst

面向 Kubernetes 资源预测、Pod 弹性扩容与容量规划的平台，包含以下业务模块：

- 数据源与预测：Prometheus 数据源、指标映射与内置指标预设、一键演示（程序生成的合成数据）、算法管理（平台级统一配置并自动应用到所有模块）、模块（Kubernetes 工作负载，自带多指标来源与预测周期）、加权综合计算、动态多约束副本数求解、评分排名与自动选择、回测、推荐与实测评估、统一指标查询。
- kube-resource-fcst-operator：读平台的自动模块并写副本（替代 HPA/KEDA；没有 CRD，决策来源只有平台），并导出 kube_resource_fcst_operator_* 指标。
- 系统管理：系统参数、登录日志、操作日志、API 凭据。
- 授权管理：用户、角色、菜单和 API 权限。
- 任务中心：任务定义、调度、执行记录和失败重试。

个人资料页是隐藏的登录自助页面。进度与未验证项见 docs/platform-progress.md。

## 项目官网

中英文官网与文档中心位于 [site/](site/README.md)，编辑源位于 `website/`。首页介绍项目与功能，文档按入门、核心概念、使用指南、运维管理分组。修改后生成页面，再启动预览：

```sh
python scripts/build-site.py
python -m http.server 4173 --bind 127.0.0.1 --directory site
```

访问 `http://127.0.0.1:4173`。GitHub Pages 工作流位于 `.github/workflows/pages.yml`，发布步骤见 [官网维护说明](site/README.md)。

## 开发与启动

后端沿用 Go、Gin、GORM、Cobra、Casbin；管理界面沿用 Vue 3、Naive UI、Pinia、Elegant Router。

```sh
cd web
pnpm install --frozen-lockfile
pnpm build
cd ..
make package-web
go build -o target/bin/kube-resource-fcst ./cmd
target/bin/kube-resource-fcst server --config config.yaml --migration
target/bin/kube-resource-fcst all --config config.yaml
```

也可以分别启动 `kube-resource-fcst server` 和 `kube-resource-fcst job`。配置与数据库驱动沿用原有方式。

配置环境变量统一使用 `KUBE_RESOURCE_FCST_` 前缀，凭据加密密钥使用 `KUBE_RESOURCE_FCST_SECRET_KEY`。
API 客户端来源与开发者凭据请求头使用 `X-kube-resource-fcst-Client`、`X-kube-resource-fcst-AccessKey` 和 `X-kube-resource-fcst-AccessSecret`。
已有安装应保持数据库路径和密钥值不变，仅更新配置变量名称；品牌更名不迁移或重置运行数据库。
环境变量兼容读取、API 客户端及 Kubernetes 对象切换说明见 [名称与升级说明](docs/renaming.md)。

## 数据库升级

已有安装请先备份数据库，再执行 `kube-resource-fcst server --config config.yaml --upgrade`。
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
