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
