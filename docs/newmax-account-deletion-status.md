# NewMax 控制面账号销毁本地续修记录

2026-10-01。已获 Jay 授权在正常本地 main 修改后端并部署专属隔离测试栈。**真实数据库基础链路、Redis 旧缓存/停机、APISIX 数据面及部分跨服务恢复矩阵已通过；非空物理文件、全并发与多实例等发布门禁仍未通过，不可直接上线。** 早期记录保留，最新认证错误码修复及实际验证见文末。

## 接口

`POST /internal/newmax/accounts/delete`，Bearer `NEWMAX_ACCOUNT_DELETION_KEY`，严格 `{uid:控制面账号ID}`。UID 不等于共享 root user_id。空配置/错误认证、未知字段、非法 UID、尾随 JSON 或超限正文拒绝。终态必须同 UID 的 `success:true,accountDeleted:true`。

历史归属未确认、仍有在途操作、未知文件归属或实际清理失败都保持 pending。历史未确认也先封禁已知凭据，不能因不敢清历史而继续允许已知 key 使用。

## 本轮补修

- `controller/newmax_account_deletion.go` 与新增测试：受保护端点、冻结后旧 key、在途等待、同目标重复删除及其他账号隔离。
- `model/newmax_account_cleanup.go`：清理 token ID 集合必须与该 UID 的全部精确归属一致；其他账号、遗漏或重复 ID 拒绝。
- `model/newmax_account_deletion.go`、新 `newmax_operation_owner.go`：登记进程 PID、内核启动/PID 命名空间（macOS boot session）。真实子进程测试验证活进程阻塞、退出后安全恢复。
- 未知/旧版登记、不同机器/容器/内核启动、权限不足、不支持平台或 PID 重用保持 pending，不能因年龄或心跳丢失就认定操作停止。
- 新 `model/newmax_image_task_directory.go`、`model/main.go`、`controller/image_task.go`：写入图片请求文件前持久登记随机目录归属，覆盖任务行尚未落库时崩溃遗留文件。
- 新 `controller/newmax_image_cleanup.go`：固定存储根按完整可靠归属核对后只删目标目录；未知旧目录或冲突阻塞。测试验证崩溃文件清理与其他账号保留。
- 原有缓存读取/回填、图片任务、正文及主要个人记录保护继续保留；token/私人正文/任务及文件清理，必要用量日志去标识保留。最小凭据墓碑和归属不能清掉后允许旧身份复活。

## 正式构建与测试

本机没有 Bun，使用 npm exec 提供 Bun 1.4.2，未全局改包管理配置。

```sh
# 在 web/ 中
bun install --frozen-lockfile --linker isolated
# 按项目 Makefile 的环境，在 default/ 和 classic/ 分别运行
VITE_REACT_APP_VERSION=<VERSION 文件内容> bun run build
```

初始 hoisted 安装使 date-fns-tz 1.3.8 解析到默认前端的 date-fns 4，classic 构建失败。使用隔离依赖安装后成功；没有改依赖清单或锁文件，没有放空 dist 假装通过。两套 dist 均来自真实正式构建。

验证：

- `TEST_MYSQL_DSN= TEST_POSTGRES_DSN= go test -json ./...`：780 项通过，2 项外部数据库迁移兼容用例因环境未配置跳过，无失败。
- `go build ./...`：包含根包真实嵌入资源的完整编译通过。
- `go vet ./model ./controller ./router ./middleware`：通过。
- `git diff --check`：通过。
- `go vet ./...`：失败于未修改的 common 中复制 mutex/IPv6 地址构造，以及多个 relay/channel adaptor 的不可达代码。这些文件相对 HEAD 未变；没有顺手扩展修复，不能称全仓 vet 全绿。

## 仍需发布前完成

1. 隔离主站 MySQL 专项、影子 PostgreSQL 迁移及控制面锁/供给已通过；仍需各业务写入触发器的完整并发验收，SQLite 不代替多实例数据库行为。自动迁移必须包含新目录表及操作归属列。
2. 真实 Redis 旧缓存回填/停机、APISIX 数据面、OC 停机、持久 pending 后主站/网关重启、客户端响应丢失及手动 cron 已通过；活跃写入中强杀、服务间丢包、网络分区和完整晚写矩阵仍未覆盖。
3. 精确历史 token 和旧文件归属审计，不能按邮箱或 root user_id 批量扩大范围，不能批量设 history_verified。
4. 所有写入实例升级并共享有归属的图片存储根；单个副本不能只清自己磁盘就宣称其他副本已清理。跨容器/旧内核环境遗留登记先停止旧写入实例并离线核实，不盲删。
5. 既有全仓 vet 告警仍需单独决定是否整改。

完整跨服务记录：`/Users/jaylee/Documents/GitHub/niumaapi/docs/account-deletion-status.md`。未修改前端源码；未提交、推送、部署生产或删除真实账号。独立测试部署不等于生产上线，当前在父会话继续，不调用阻塞子会话完成工具冒称已完成全部门禁。

## 真实缓存验收发现的认证分类修复（2026-10-01）

测试栈真实认证预热 Redis token 缓存后，删号/精确回填旧缓存均正确拒绝访问，但只读和转发入口把 `ErrNewmaxAccountDeleted` 当成数据库异常返回500。

- 新增 `middleware/account_deletion_auth_test.go`：真实临时 SQLite 墓碑，覆盖只读/转发 × 已删除/真实数据库关闭。先复现两项预期401实际500，再验证修复；两个数据库故障场景仍须500，任何失败都不进入业务处理器。
- `middleware/auth.go`：只读认证将删除墓碑与不存在凭据一并归为401。
- `model/token.go`：`ValidateUserToken` 将删除墓碑归为 `ErrTokenInvalid`，不再包装成 `ErrDatabase`；其他数据库异常仍原样分类。
- 没有绕过墓碑查询、放宽凭据或仅凭缓存认定账号正常。

验证：相关 middleware/model 专项通过；全量 `go test -json ./...` **785 项通过、2 项外部数据库兼容测试跳过**；`go vet ./middleware ./model` 与完整 `go build ./...` 通过。既有全仓 vet 告警未修，不能称全仓 vet 全绿。

在限额构建器重新生成正式测试镜像，仅替换 `/opt/newmax-account-deletion-test` 的 new-api。新合成账号实际删除后回填原16字段token缓存，两个入口均401，另一个账号200；先前四个已删除账号的墓碑在新镜像下也均401。影子token按uid精确清理，未把共享root下其他账号一起删除。

跨服务累计96项检查通过（包含重复状态和复验，不是96种故障），28项隔离护栏/状态保存测试通过。OC停机、Redis停机、主站/网关持久pending后重启、客户端终态回执丢失及手动cron恢复已验收；自动调度器、非空物理文件、活跃写入强杀、网络分区和多实例共享存储仍未验收。

生产容器ID/启动时间均未变，最终磁盘约52.95GiB。脱敏证据在移动端 `docs/app-store/2026-10-01-account-deletion-cache-recovery-evidence.json`，详细日志在 `niumaapi/deploy/account-deletion-test/reports/`。外部项目修改不属于移动端工作区一键撤销。
