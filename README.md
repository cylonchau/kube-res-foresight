# KubeForecast

面向 Kubernetes 资源预测、Pod 弹性扩容与容量规划的平台，包含以下业务模块：

- 数据源与预测：Prometheus 数据源、指标映射与内置指标预设、一键演示（程序生成的合成数据）、算法管理（平台级统一配置并自动应用到所有模块）、模块（Kubernetes 工作负载，自带多指标来源与预测周期）、加权综合计算、动态多约束副本数求解、评分排名与自动选择、回测、推荐与实测评估、统一指标查询。
- kubeforecast-operator：按 PredictiveScaler CRD 控制副本（替代 HPA/KEDA），决策来源为平台、Prometheus 或固定值，并导出 kubeforecast_operator_* 指标。
- 系统管理：系统参数、登录日志、操作日志、API 凭据。
- 授权管理：用户、角色、菜单和 API 权限。
- 任务中心：任务定义、调度、执行记录和失败重试。

个人资料页是隐藏的登录自助页面。进度与未验证项见 docs/platform-progress.md。

## 开发与启动

后端沿用 Go、Gin、GORM、Cobra、Casbin；管理界面沿用 Vue 3、Naive UI、Pinia、Elegant Router。

```sh
cd web
pnpm install --frozen-lockfile
pnpm build
cd ..
make package-web
go build -o target/bin/kubeforecast ./cmd
target/bin/kubeforecast server --config config.yaml --migration
target/bin/kubeforecast all --config config.yaml
```

也可以分别启动 `kubeforecast server` 和 `kubeforecast job`。配置与数据库驱动沿用原有方式。

配置环境变量统一使用 `KUBEFORECAST_` 前缀，凭据加密密钥使用 `KUBEFORECAST_SECRET_KEY`。
API 客户端来源与开发者凭据请求头使用 `X-KubeForecast-Client`、`X-KubeForecast-AccessKey` 和 `X-KubeForecast-AccessSecret`。
已有安装应保持数据库路径和密钥值不变，仅更新配置变量名称；品牌更名不迁移或重置运行数据库。

## 数据库升级

已有安装请先备份数据库，再执行 `kubeforecast server --config config.yaml --upgrade`。
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
