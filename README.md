# KubeForecast

面向 Kubernetes 资源预测、Pod 弹性扩容与容量规划的管理基础框架，当前保留四个业务模块：

- 工作流引擎：流程定义、实例、状态流转与动作执行。
- 系统管理：系统参数、操作日志、API 凭据。
- 授权管理：用户、角色、菜单和 API 权限。
- 任务中心：任务定义、调度、执行记录和失败重试。

个人资料页是隐藏的登录自助页面。Telegram 适配器仅作为工作流通知和身份绑定的内部依赖，没有独立管理菜单、启动命令或程序入口。

当前提供管理、授权、工作流和任务基础设施；集群指标接入、预测模型及自动扩容控制器尚未实现。

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
该显式升级会移除已退役模块的表、菜单、任务和设置；保留通用工作流、用户、权限、自定义通用任务及审计记录。普通启动不删表。
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
