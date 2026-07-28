# AI 决策解析健壮性（形状容错）

## 核心原则

**一个纯说明性字段的形状，绝不能否决一整批决策。**

`kernel/engine.go` 的决策结构体里，凡是"由模型填写"的字段，其 JSON 形状都是**不可控输入**，
而不是接口契约。模型今天返回数组、明天返回裸字符串，都属于正常波动。
解析层必须容错；用 strict unmarshal 去"约束"模型，代价是丢单。

判据（登记新字段时照此自查）：
- 字段值会不会进入下单参数？→ 会：可以严格，形状错就该拒绝。
- 只用于展示/记录/复盘？→ 不会：必须容错。
- 字段由我们自己的代码填（例如币池、指标计算）？→ 无形状风险，不必加容错。

---

## 2026-07-28 缺陷：一个 `alignment_notes` 丢掉整个周期 3416（提交 309c4ea）

线上 17:32:34，周期 3416 全批决策被丢弃：

```
json: cannot unmarshal string into Go struct field
AIEntryProtectionRationale.alignment_notes of type []string
→ failed to extract decisions
```

模型把 `alignment_notes` 返回成裸字符串而不是数组。该字段**从不参与下单**，
纯粹是"对齐说明"文本。代价是整批被丢，**其中含一笔 ZECUSDT open_long**。

### 修法

新增 `AIStringList`（`kernel/engine.go`），带容错 `UnmarshalJSON`：

| 输入 | 结果 |
|---|---|
| `["a","b"]` | 原样 |
| `"a"` | `["a"]`（单元素） |
| `null` / `[]` / `""` / `"   "` | `nil`（0 元素） |
| 其他标量（如 `42`） | `["42"]` 字面量 |

沿用本文件里已有的 `AIQualityScore.UnmarshalJSON` 先例（同一套"模型形状漂移"应对法）。

**不是哪漏补哪**：三个"由 AI 填写"的 `[]string` 字段一起换，而不只换报错那个：
- `AIEntryProtectionRationale.AlignmentNotes`
- `AIEntryTimeframeContext.Lower`
- `AIEntryTimeframeContext.Higher`

`CandidateCoin.Sources` **故意保持 `[]string`** —— 它由币池代码填，不经过模型，没有形状风险。
（加容错等于给自己的代码留一条静默降级路径，反而有害。）

### 验证

`kernel/ai_string_list_test.go` 5 个用例，含**真正失败的那一层**
（`TestDecisionBatchSurvivesBareStringRationale`：整批 WLDUSDT hold + ZECUSDT open_long
都必须存活），用的是线上原文。

**反向对照已跑**：把三个字段类型改回 `[]string`，5 个用例中 3 个复现出线上那条一模一样的
错误串；改回修复版全过。全套 `kernel`/`trader`/`api`/`store` 绿。

### 归类

这是"**用不该用的那个量做判断**"错误族在解析层的一个实例：
用**字段形状**（不可控输入）去判断**整批决策是否可用**（业务结论）。
与保护系统 v1.16.27 三缺陷同源 —— 见 [unified-protection-system.md](unified-protection-system.md)。

---

## 2026-07-28 结构性根因：整数组原子解码（提交 c21e3c3）

上面那个修复只解决了"这次踩到的三个字段"。**逐个字段加容错是打地鼠，永远补不完**
（这个文件里已经有 13 个容错解析器，说明形状漂移是反复发生的常态）。
真正该问的是：*为什么一个字段的错误能杀掉整批*。

`extractDecisions` 用 `json.Unmarshal(jsonContent, &decisions)` **整数组一次性解码**。

### 实测行为（探针，3 元素数组，中间那个有一个坏字段）

```
len(decisions) = 2        ← 不是 3
  [0] symbol="AAAUSDT" action="hold"
  [1] symbol=""        action=""     ← 被清零：Decision.UnmarshalJSON 提前 return，
                                        *d = Decision(a) 从没执行
  [2] 根本没被解码                    ← 解码在这里整体中止
```

**比表面严重得多**：一个说明性字段不只丢掉自己那条决策，还丢掉**排在它后面所有**决策
—— 包括 `close`。**漏平比漏开危险**，这就是"整数组原子性"在这里是错误默认值的原因。

关键区分（当前代码没做，是根因）：
- **类型错误**：结构完好、数据可信，只有个别字段形状不对 → 应逐元素隔离
- **语法错误**：结构都拆不开（`len=0`）→ 没有可信数据，应整批失败

### 修法

`decodeDecisionArray`：先把数组拆成 `[]json.RawMessage`（廉价结构解析，只对真语法错误失败），
再**逐元素**解码。好的存活，坏的只损失自己。

刻意保留的行为（三条反向对照都有测试）：
| 情形 | 行为 |
|---|---|
| 全部正常 | 与从前完全一致，不报降级 |
| 真语法错误 | 仍整批失败（无可信数据） |
| 一条都救不回 | 仍**报错**，绝不返回空集 —— 否则会被误当作"AI 决定什么都不做" |

**刻意不做**：从失败元素里捞 symbol/action 拼一条部分决策。它的数值字段已经静默清零，
可能下错单 —— **丢掉才是安全方向**。

降级必须可见不可静默：丢弃行记日志（带 symbol/action），并经既有 `ParseFallbackReason`
通道上报为 `partial_parse_dropped_N_of_M`（该通道已接到 `auto_trader_loop.go` 的周期复盘记录）。

### 验证

`kernel/decision_partial_parse_test.go` 4 用例，坏元素**故意排在 close 之前**（危险排布）。
反向对照：把 `decodeDecisionArray` 改回原子解码，恰好是那条用例失败。
