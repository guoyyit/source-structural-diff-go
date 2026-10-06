# source-structural-diff-go

源码结构差异比对与审查基线服务。对 Go 源码树做**声明级**结构差异分析，
并以增量方式维护已提交的审查基线。注释、空白、格式化与**冗余括号**变化
不会影响比对结论；重命名与跨文件移动基于结构相似性证据判定，并附带
置信度与可核查的证据列表。

## 构建与测试

```sh
go build ./...
go test ./...
# 并发正确性验证
go test ./... -race -count=1
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
ssdiff diff-workspace -store .ssdiff -root /path/to/source HEAD
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

结构指纹通过对 AST 做位置无关的规范化序列化得到，因此：

- **注释、空白、换行与 gofmt 风格差异不会改变任何分类结论**；
- **冗余括号不变性**：仅增加或删除冗余括号（含嵌套、多层括号，如
  `(x)`、`((a + b))`、`if (cond) {}`）判定为未变化。括号只改变语法
  分组，运算优先级已由 AST 结构表达；改变求值顺序的括号
  （如 `a*(b+c)` → `a*b+c`）仍会被正确判定为 `modified`。

## 相似性证据与置信度

判定完全基于结构相似性证据，**同名本身只是弱证据**：结构无关的同名符号
得分低于候选阈值，会被保留为新增 + 删除，而不会被误判为移动或改名。

相似度得分由以下证据加权组成（见 `internal/diff/diff.go` 的 `similarity`）：

- `exact`：token 流逐一致（仅格式/注释/冗余括号不同）——决定性证据，
  置信度直接为 1.0，且不受符号规模限制
- `shape`：归一化声明名后的结构指纹完全一致（得分提到 0.95）
- `structure`：AST 节点类型直方图的加权 Jaccard 相似度（权重 0.55）
- `identifiers`：标识符使用集合的加权 Jaccard 相似度（权重 0.25）
- `name` / `receiver`：符号名、接收者类型一致（弱证据，合计权重 0.15；
  没有任何结构证据时不产生基础分）

**置信度与证据覆盖范围一致**：符号的结构证据量（AST 节点、标识符与
字面量总数，取两侧较小值）低于 `MinEvidenceNodes`（默认 20）时，即使
shape 完全一致，置信度也会被压到匹配阈值之下并进入候选匹配——
「仅参数命名不同的小型符号」不会获得高置信度移动结论。只有 `exact`
证据豁免该上限，因为逐 token 一致在任何规模下都不可能是巧合。

## 候选匹配的进入条件

阈值（`diff.DefaultOptions`，可用 `diff.Options` 调整）：

- **≥ 0.85（MatchThreshold）**：判定为 `renamed` / `moved`，报告中给出
  全部证据与置信度。
- **0.5 – 0.85（CandidateThreshold）**：证据不足，归入**候选匹配**
  （`candidate: true`），不会静默错分；条目中仍给出最佳猜测类别与证据。
- **< 0.5**：不匹配，按 `added` / `deleted` 处理。

此外，证据覆盖不足（符号太小）的匹配即使得分达到 0.85 也会被压入候选。
候选条目携带 `note` 字段，以可区分的方式说明进入候选的原因：

- `insufficient structural evidence: only N evidence nodes ...`——覆盖不足被压入；
- `best-effort match: score X below match threshold 0.85`——得分本身不足。

匹配采用全局贪心：所有候选对按得分降序排列，每个旧/新声明最多配对一次，
同分时按键序裁决，保证结果确定可复现。

## 基线的提交与查询

### 提交

- 基线是**内容寻址**的不可变快照：ID = SHA-256(parent ID + 树模型)。
  同一棵树在同一父基线上重复提交得到同一 ID（幂等）；无变化提交直接
  返回当前 HEAD，不产生新基线。
- **增量维护**：文件级缓存键是 token 流哈希（对注释/格式化不变）。只有
  内容真实变化的文件会被重新解析；未变化文件直接复用上一基线或磁盘缓存
  中的模型（进度事件的 `parsed` 字段可观测实际解析数）。增量结果与
  全量重算逐条一致——分类、证据、置信度与排序均有测试保证，覆盖
  跨文件移动两端同时变化、文件删除后候选关系失效等场景。
- **原子生效**：基线文件与 HEAD 均通过「写临时文件 + fsync + rename」
  提交。任何中途失败或进程崩溃都不会产生半提交状态；重启后已提交基线
  可继续使用（无需迁移），残留的临时文件与不可达基线会在 `Open` 时被
  清理，恢复过程不产生重复条目、不丢失条目。

### 查询

- `Diff(oldID, newID)` 对比任意两个已提交基线；`DiffWorkspace(baseID, root)`
  对比基线与工作区。ID 传 `HEAD` 或空串表示当前基线快照。
- 提交与查询可**并发**进行：查询通过原子快照指针读取，任何时刻看到的
  都是单个完整基线，不会出现半提交状态；同一对版本在并发压力下重复
  比对的输出逐条一致。
- 报告按（文件, 符号, 类别）稳定排序；同一对版本重复比对的输出逐条一致。

### 取消与超时

- `context.Context` 取消与超时贯穿扫描、解析与提交；取消不会留下部分基线。
- 任何提前结束的路径都会**回收全部已启动的工作协程**（结果通道排空后
  才返回），反复触发取消/超时场景协程数不增长，后续提交与查询不受影响。

### 大规模源码树

- 解析在有界 worker 池上并行（`Options.Workers`，默认 CPU 数），内存占用
  与并发度挂钩。
- `Options.Progress` 回调报告扫描总数、每文件完成事件（含是否实际解析）
  与慢文件事件（`Options.SlowFileThreshold`）。

## 报告格式

文本输出每行一条分类结果；`-json` 输出完整报告：

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
      "confidence": 0.95,
      "evidence": [
        {"kind": "shape", "detail": "identical structure modulo declared name", "score": 1}
      ]
    },
    {
      "category": "moved",
      "kind": "func",
      "name": "sum",
      "oldName": "add",
      "file": "sub/b.go",
      "oldFile": "pkg/b.go",
      "confidence": 0.84,
      "evidence": [
        {"kind": "structure", "detail": "AST node-type histogram similarity", "score": 1},
        {"kind": "identifiers", "detail": "identifier usage similarity", "score": 1},
        {"kind": "shape", "detail": "identical structure modulo declared name", "score": 1}
      ],
      "candidate": true,
      "note": "insufficient structural evidence: only 16 evidence nodes (need 20); confidence capped below match threshold"
    }
  ],
  "summary": {"unchanged": 1, "moved": 2}
}
```

报告字段只做向后兼容的扩展（如 `candidate`、`note`），旧字段语义不变。

## 代码布局

- `internal/model` — 声明与源码树的结构模型
- `internal/syntax` — Go 源码解析、规范化指纹（括号透明）、token 流哈希
- `internal/diff` — 结构比对、相似度证据与分类、候选匹配
- `internal/baseline` — 基线存储：原子提交、HEAD、解析缓存、崩溃恢复
- `internal/service` — 增量提交、并发查询、取消回收、进度观测
- `cmd/ssdiff` — 命令行入口
