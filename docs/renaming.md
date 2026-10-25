# kube-resource-fcst 名称与升级说明

项目名称统一为 `kube-resource-fcst`。本说明只涉及名称与接入标识变化，不要求重建业务数据库。

| 用途 | 当前名称 |
|---|---|
| 展示名称、CLI、平台二进制 | `kube-resource-fcst` |
| Operator 二进制与 Helm chart | `kube-resource-fcst-operator` |
| Go 模块 | `github.com/cylonchau/kube-resource-fcst` |
| 配置环境变量 | `KUBE_RESOURCE_FCST_*` |
| 凭据加密密钥 | `KUBE_RESOURCE_FCST_SECRET_KEY` |
| HTTP 客户端头 | `X-kube-resource-fcst-Client` |
| HTTP 开发者凭据头 | `X-kube-resource-fcst-AccessKey` / `X-kube-resource-fcst-AccessSecret` |
| Authorization scheme | `kube-resource-fcst-AK <key>:<secret>` |
| WebSocket 子协议 | `kube-resource-fcst.jwt` |
| Platform / Operator 指标 | `kube_resource_fcst_*` / `kube_resource_fcst_operator_*` |
| PredictiveScaler API group | `autoscaling.kube-resource-fcst.io/v1alpha1` |
| 新安装 SQLite 默认文件 | `kube-resource-fcst.db` |

## 数据库与密钥

- 保持已有数据库连接、路径、账号和加密密钥的值不变。更换环境变量名称不等于更换密钥值。
- 配置读取、凭据加密、Operator 平台凭据与诊断入口保留 `KUBEFORECAST_*` 的兼容读取；同一项的新名称优先。旧名称仅用于升级兼容，不再作为配置示例。
- 若配置指向默认 `kube-resource-fcst.db`，该文件尚不存在，但同目录已有 `kubeforecast.db`，平台继续使用已有文件，不移动文件、不新建空库。新文件已经存在或使用自定义路径时，遵循所配置的文件。
- 不重命名正在使用的 SQLite 主文件、WAL 或 SHM；需要统一物理文件名时，先停服务、完成备份，再以数据库的安全备份/恢复方式处理，并明确更新配置。
- 现有数据库中的业务与审计记录不做品牌文本批量覆盖。登录会话与已有 TOTP 秘钥不因展示名称变化而重置。

## API 与监控客户端

同步更新调用方的请求头、Authorization scheme、WebSocket 子协议及 Prometheus 查询/告警中的指标前缀，再切换服务端。旧指标名称不作为双份别名导出。指标历史仍保留在原监控存储中，跨切换窗口查询时分别读取相应前缀。

## Kubernetes 与 Helm

CRD API group、命名空间、Service、Secret、RBAC、Lease 和 Helm 命名均已更新。它们是集群中的对象标识，改名不会自动搬迁已有对象。

1. 记录原工作负载、策略、边界、Secret 与实际副本状态，备份原 PredictiveScaler 的 spec。
2. 将原控制器停止写入目标，并将原策略保持在观察或旁路状态；确认 HPA/KEDA 与其他控制器不会竞争副本。
3. 安装新 CRD/RBAC，按新 API group 重建策略，或采用平台模块驱动方式。新部署先保持 Observe，并更新平台地址、认证与引用的 Secret。
4. 检查新配置、推荐读取和冲突状态后，再授权唯一的控制器执行。不得让新旧 Deployment 因 Lease 名称不同而同时调整同一工作负载。
5. 确认切换后再按维护流程处理旧资源。不要直接删除仍含有效对象的旧 CRD，也不要直接对旧 Helm release 使用可能引发重建的升级方式。

仓库仅更新源码与清单，不自动操作现有集群，不创建或重命名 GitHub 仓库，不更改 Git remote。
