# source-structural-diff-go

源码结构差异比对与审查基线服务。对 Go 源码树做**声明级**结构差异分析，
并以增量方式维护已提交的审查基线。注释、空白、格式化与**冗余括号**
变化不会影响比对结论；重命名与跨文件移动基于结构相似性证据判定，
并附带与证据强度、覆盖范围一致的置信度。

## 构建与测试

```sh
go build ./...
go test ./...
```

## 快速开始（CLI）

```sh
# 提交当前源码树为基线（增量：只重新解析有变化的文件）
ssdiff commit -store .ssdiff -root /path/to/source

# 查看基线列表与当前 HEAD
ssdiff list -store .ssdiff
ssdiff head -store .ssdiff

# 对比两个已提交基线（ID 可用 HEAD 指代当前基线）
ssdiff diff -store .ssdiff <OLD_ID> <NEW_ID>
ssdiff diff -store .ssdiff -json <OLD_ID> HEAD

# 对比已提交基线与当前工作区
ssdiff diff-workspace -store .ssdiff -json HEAD -root /path/to/source
```

提交时加 `-v` 可观察扫描进度与慢文件事件。

## 分类语义

比对以**顶层声明**为最小单位（func、method、type、var、const；多变量的
var/const 声明按名字拆分）。每个声明在两个版本之间被分类为：

| 分类 | 含义 |
| --- | --- |
| `unchanged` | 同一文件同一符号，结构指纹完全一致 |
| `modified` | 同一文件同一符号，结构指纹发生变化（注释/格式化/冗余括号不算） |
| `added` | 新版本中出现、且无足够证据匹配旧声明的符号 |
| `deleted` | 旧版本中消失、且无足够证据匹配新声明的符号 |
| `renamed` | 同一文件内符号改名，结构相似性证据达到阈值 |
| `moved` | 符号跨文件移动（可同时改名），结构相似性证据达到阈值 |

结构指纹通过对 AST 做位置无关的规范化序列化得到，因此**注释、空白、
换行与 gofmt 风格差异不会改变任何分类结论**。

### 冗余括号不变性

序列化时 `ParenExpr` 节点被完全跳过：括号只是重复表达了树结构已经
编码的分组信息。因此同一表达式**仅增加或删除冗余括号（含嵌套、多层
括号）时判定为未变化**，且该结论不受空白、注释与格式调整影响。会改变
求值结构的括号（如 `2*3+4` → `2*(3+4)`）因 AST 形状本身不同，仍会被
判定为 `modified`。

## 相似性证据与置信度

重命名/移动判定完全基于结构相似性证据，**同名本身只是弱证据**：结构
无关的同名符号得分低于候选阈值，按新增 + 删除处理，不会被误判为移动
或改名。

每个候选对都会生成可核查的证据条目（`evidence` 数组）：

- `structure`：AST 节点类型直方图的加权 Jaccard 相似度，附共享/总数
  节点计数（如 "share 25 of 25 nodes"）
- `identifiers`：标识符使用集合的加权 Jaccard 相似度，附共享/总数
- `shape`：归一化声明名后的结构指纹完全一致，附覆盖的节点数
- `coverage`：两个声明中较小的结构规模（AST 节点数）及覆盖系数
- `name` / `receiver`：符号名、接收者类型一致（弱证据）

置信度计算（见 `internal/diff/diff.go` 的 `similarity`）：

```
coverage  = min(1, 较小声明节点数 / 16)
cap       = 0.5 + 0.5*coverage        // 该候选对的置信度上限
weighted  = min(cap, 0.60*structure + 0.28*identifiers + 0.08*name + 0.04*receiver)
shape     = cap * (0.75 + 0.25*identifiers)   // 仅当 shape 指纹一致
score     = max(weighted, shape)
```

**置信度与证据强度及覆盖范围一致**：结构规模越小，可达置信度上限越低。
外形（shape）相同不足以支撑高置信度——空函数的形状与任何空函数都
一致，因此小型符号（如仅参数命名不同的 `func helper(n int)`）即使
shape 完全匹配也只能进入候选，不会得到高置信度移动/改名结论。

## 候选匹配的进入条件

阈值（`diff.DefaultOptions`，可用 `diff.Options` 调整）：

- **≥ 0.85（MatchThreshold）**：判定为 `renamed` / `moved`。
- **0.5 – 0.85（CandidateThreshold）**：证据不足，归入**候选匹配**
  （`candidate: true`），不会静默错分；条目仍给出最佳猜测类别、全部
  证据，以及 `note` 字段说明为何不能自动判定。
- **< 0.5**：不匹配，按 `added` / `deleted` 处理。

匹配采用全局贪心：所有候选对按得分降序排列，每个旧/新声明最多配对
一次，同分时按键序裁决，保证结果确定可复现。

## 基线的提交与查询

### 提交

- 基线是**内容寻址**的不可变快照：ID = SHA-256(parent ID + 树模型)。
  同一棵树在同一父基线上重复提交得到同一 ID（幂等）；无变化提交直接
  返回当前 HEAD，不产生新基线。
- **增量维护**：文件级缓存键是 token 流哈希（对注释/格式化不变）。只有
  内容真实变化的文件会被重新解析；未变化文件直接复用上一基线或磁盘缓存
  中的模型（进度事件中 `reused: true` 可观测）。增量结果与全量重算逐条
  一致（分类、证据、置信度与排序均有测试保证）。缓存按指纹算法版本隔离
  （`cache/v2/`），升级后旧缓存条目不会被误用。
- **原子生效**：基线文件与 HEAD 均通过「写临时文件 + fsync + rename」
  提交。任何中途失败或进程崩溃都不会产生半提交状态；重启后已提交基线
  可继续使用，残留的临时文件与不可达基线会在 `Open` 时被清理，不会产生
  重复或丢失条目。

### 查询

- `Diff(oldID, newID)` 对比任意两个已提交基线；`DiffWorkspace(baseID, root)`
  对比基线与工作区。ID 传 `HEAD` 或空串表示当前基线快照。
- 提交与查询可**并发**进行：查询通过原子快照指针读取，任何时刻看到的
  都是单个完整基线，不会出现半提交状态；同一对版本在并发压力下重复比对
  的输出逐条一致。
- 报告按（文件, 符号, 类别）稳定排序。

### 取消与超时

- `context.Context` 取消与超时贯穿扫描、解析与提交；取消/超时不会留下
  部分基线，HEAD 保持不变。
- 所有已启动的工作协程在返回前全部回收（worker 在 ctx 结束时停止发送，
  收集协程排空结果通道直至关闭），反复取消/超时不会造成协程或内存
  泄漏，后续提交与查询不受影响。

### 大规模源码树

- 解析在有界 worker 池上并行（`Options.Workers`，默认 CPU 数），内存占用
  与并发度挂钩。
- `Options.Progress` 回调报告扫描总数、每文件完成事件（含是否复用）与
  慢文件事件（`Options.SlowFileThreshold`）。

## 报告格式

文本输出每行一条分类结果；`-json` 输出完整报告。报告字段只做向后兼容
的扩展（本轮新增 `note` 字段与 `coverage` 证据类型）：

```json
{
  "entries": [
    {
      "category": "moved",
      "kind": "func",
      "name": "SumAll",
      "oldName": "ComputeTotal",
      "file": "sub/a.go",
      "oldFile": "pkg/a.go",
      "confidence": 0.9583,
      "evidence": [
        {"kind": "structure", "detail": "AST node-type histograms share 25 of 25 nodes", "score": 1},
        {"kind": "identifiers", "detail": "shared 10 of 12 identifier uses", "score": 0.8333},
        {"kind": "coverage", "detail": "structural size 25 and 25 nodes (coverage 1.00)", "score": 1},
        {"kind": "shape", "detail": "identical structure modulo declared name over 25 nodes", "score": 0.9583}
      ]
    }
  ],
  "summary": {"unchanged": 1, "moved": 1}
}
```

## 兼容性说明

- 提交与查询接口（CLI 与 `service.Service` API）保持不变；已存在的基线
  数据升级后无需迁移即可继续加载与比对。
- 本轮修复了冗余括号指纹：升级后新提交中声明的指纹不再包含括号节点。
  跨升级点比对时，含括号的声明可能被重新分类一次（通常为 `modified`），
  重新提交后即稳定；解析缓存已按算法版本隔离，不会出现新旧混用。

## 代码布局

- `internal/model` — 声明与源码树的结构模型
- `internal/syntax` — Go 源码解析、规范化指纹（冗余括号透明）、token 流哈希
- `internal/diff` — 结构比对、相似度证据与覆盖范围感知的置信度
- `internal/baseline` — 基线存储：原子提交、HEAD、版本化解析缓存、崩溃恢复
- `internal/service` — 增量提交、并发查询、取消回收、进度观测
- `cmd/ssdiff` — 命令行入口
