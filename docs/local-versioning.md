# 项目时光机（本地版本管理）

Curvature 自带一套**不依赖 git** 的本地版本管理：内容寻址存储 + 快照提交 + 文件级 diff + 任意时间点回滚。设计取舍与决策记录见 [`local-versioning-prd.md`](./local-versioning-prd.md)。

## 概念对照

| 时光机 | git 近似物 | 说明 |
|---|---|---|
| 快照 | commit | 一次工作区全量记录 |
| 时间线 | branch | 可切换的独立历史 |
| 书签 | tag | 指向某个快照的名字 |
| 自动检查点 | — | 独有的**无父提交**安全网，见下 |
| 恢复 | checkout / revert | 默认 Revert：历史只向前，不丢记录 |

## 三种历史，互不干扰

这是理解本功能最重要的一点：

1. **用户快照**（主时间线）—— 你主动点「新建快照」或 `-versioning snapshot` 产生的。走 `Log`、出现在时间线里，是项目的正式叙事。
2. **自动检查点** —— 会话轮次结束、kanban 任务进入终态时**自动**记录的。刻意**不出现在** `Log` 与用户时间线中，只能通过检查点列表查看。默认开启、30 秒去抖。
3. **恢复产生的安全存档** —— 回滚前先记录当前状态，所以回滚本身也可回滚。它落在主时间线上，因此**永远不会被保留策略清理掉**。

## 自动检查点与保留策略

自动检查点按去抖产生，不加约束会无限增长。保留策略的做法是：

- 每个检查点是 `refs/checkpoints/<时间戳>` 下的一个**无父提交**。
- 清理 = **删除最旧的 ref**，随后 GC 自然回收其对象。

无父是关键：如果检查点串成父链，最旧的会是最新的祖先、沿父指针永远可达，保留就退化成改写 DAG。无父之后**零 DAG 改写**，主时间线与安全存档结构上不可能被误删。

- 保留数量：设置里可改（默认 200，上限 100000）。
- 手动清理：`-versioning prune -keep N`（省略 `-keep` 用保留数量；`-keep 0` 清空全部）。
- 清理会**立即**回收空间（显式 prune 不套用 GC 的 1 小时宽限期，否则会看起来"什么都没释放"）。

## CLI

```bash
curvature <rootId> -versioning status              # 工作区相对 HEAD 的变更
curvature <rootId> -versioning snapshot -message "..."   # 新建快照
curvature <rootId> -versioning log -limit 20       # 用户时间线
curvature <rootId> -versioning restore -rev <hash> -mode revert|overwrite
curvature <rootId> -versioning branches            # 列出所有时间线（* 标记当前）
curvature <rootId> -versioning branch -name exp -rev <hash>   # 新建时间线（省略 -rev 从当前 HEAD 起）
curvature <rootId> -versioning checkout -name exp -mode revert|overwrite  # 切换时间线
curvature <rootId> -versioning gc                  # 回收不可达对象
curvature <rootId> -versioning prune -keep 50      # 自动检查点保留策略
curvature <rootId> -versioning verify              # 校验存储完整性
```

CLI 是**对运行中的服务**发 HTTP 请求（走本地 CLI token），不是直接操作磁盘，因此服务需在运行。

时间线名会成为 ref 的路径组成部分，因此限制为 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` —— **不支持中文等非 ASCII 字符**，超范围会被服务端以 400 拒绝。`checkout` 与 `restore` 一样默认 `revert`（历史只前进，先自动快照当前状态），`overwrite` 才会丢弃未快照的改动。

## 存储位置

`<metaDir>/versioning/`，随元数据位置走：项目模式为 `<项目>/.curvature/versioning/`，全局模式为 `~/.curvature/<rootId>/versioning/`。项目模式下 `.curvature` 已被 git 忽略，不会污染 `git status`。

对象按内容寻址（`sha256`，git 式 `<kind> <size>\0<payload>` 框架，静态 zlib 压缩），**相同内容只存一份**；清单是扁平的，提交只引用清单。

## 回滚语义

- `revert`（**默认**）：把目标快照的差异作为**一次新的提交**应用回去，历史只向前。
- `overwrite`：让工作区与目标快照完全一致，**但不动 HEAD**，因此之后 `status` 会显示为脏 —— 这是刻意的：它是一次工作区操作，不是历史操作。
- 两种模式都支持 `paths` 作用域恢复，也都会做路径穿越校验，都会**先记录当前状态为安全存档**，且都会删除目标快照之后新增的文件（只改文件内容不删文件的话，回滚会得到一个混合状态）。
- 作用域恢复时，`revert` 提交的是**实际产生的树**：作用域外的文件保持原样，不会被记成已回滚（否则 `status` 会谎报干净）。

## 开关

- 用户开关：设置面板中的「自动检查点」，或 `GET/PUT /api/preferences/versioning-auto-checkpoints`。
- 运维开关：环境变量 `CURVATURE_AUTO_CHECKPOINTS=0` —— **只能强制关闭，不能强制开启**，以免覆盖用户意愿。
- 开关在每次通知时读取，切换立即生效、无需重启。关闭期间的活动不会被补记。

## Agent 的权限边界

默认情况下，**agent 只能读历史、建快照，不能回档**：

| 动作 | agent 默认 |
|---|---|
| `info` / `status` / `log` / `diff` / `file` / `tree` | ✅ 允许 |
| `snapshot`（只增不减） | ✅ 允许 |
| `restore`（回档） | ❌ 拒绝（403） |
| `checkout` / `branch`（切时间线） | ❌ 拒绝（403） |
| `gc` / `prune` / `verify` | ✅ 允许 |

要放开，打开设置面板中的「允许 Agent 回档/切换时间线」，或：

```
PUT /api/preferences/versioning-agent-autonomy   {"enabled": true}
```

人类自己用 CLI 或界面操作**从不受限** —— 这个开关只约束 agent。

**它拦不住的**：这是一道**防误操作**的护栏，不是安全沙箱。agent 与你的终端共用同一个本地 CLI token，服务端无法从请求本身分辨两者，只能靠 agent 侧声明的标记。所以 agent 只要清掉环境变量 `CURVATURE_AGENT_SESSION`，或自己读 token 文件直接调 API，就能绕过。它挡住的是「agent 自作主张把你正在编辑的工作区回滚了」这类意外，挡不住刻意规避。

标记由 Curvature 在**拉起 agent 进程时**设置（agent 派生的 shell 都继承它）；而你在 Curvature 内置终端里的 shell 会被**主动剥掉**这个变量。所以你自己在终端里用 CLI 永远不会被拦，哪怕 Curvature 本身是从某个 agent shell 里启动的。

为什么默认关：回档会改写你正在编辑的工作区，而 agent 每轮的工作**本来就已经被自动检查点记录**了 —— 你随时能从「自动检查点」标签页自己捞回来，并不需要给 agent 回档权。

## 已知边界

- 单文件默认最大 `16 MiB`、单次快照最多 `200000` 个文件（超出部分跳过并记录）。
- 大仓库首次快照需全量扫描，成本与文件数成正比。未改动的文件靠 stat cache 跳过重读，因此**重复快照的成本主要在那次目录遍历**上。
- 前端暂无自动化测试（仓库没有前端测试运行器）。
- `/api/versioning/file` 预览上限 1 MiB，超出返回 `truncated` 标记。
- 时光机的「插件展示层」复用实时文件浏览器的 view plugin 管线；无匹配插件或插件抛错时回退纯文本。
- 分支/时间线名会成为 ref 的路径分量，限制为 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`（**不支持中文**）。
- 版本库存放位置跟随该项目的元数据位置（项目内 `.curvature/versioning/` 或 `~/.curvature/<名>/versioning/`），**不可单独配置**。因为 `.curvature/` 被 git 忽略，历史不会随 `git clone` 走；整目录拷贝在「项目内」模式会带上历史，在「Home」模式则会**静默丢失**（时光机看起来是空的）。
- `-race` 无法在当前开发机运行（缺 C 工具链），并发正确性靠定向测试覆盖而非竞态检测器。
- `backend/internal/commandexec/process_unix.go` 的进程存活守卫**未在本机编译验证**（依赖 `creack/pty`，无网络可下载），建议在 Linux/macOS 上跑一次 `go build`。
