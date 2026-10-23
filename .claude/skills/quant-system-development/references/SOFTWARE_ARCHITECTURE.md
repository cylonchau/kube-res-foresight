# Platform architecture

Visible modules: Workflow Engine, System Management, Authorization Management,
Task Center. pkg/app and pkg/apis own HTTP and admin composition; pkg/workflow
owns state machines; pkg/job owns durable scheduling and workers; pkg/model,
pkg/dao/rdb, pkg/store and pkg/migration own persistence. web owns the Vue admin.
pkg/telegram, pkg/notify and related adapters support workflow delivery internally;
there are no standalone Telegram agent commands or binaries.
hermes all starts the management server and job runtime. server and job can run
separately. Authentication, RBAC, audit and user preferences are shared services.
