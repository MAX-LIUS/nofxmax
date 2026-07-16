# Research Principles（研究纪律）

> 这份文档比任何单个策略都重要。它定义"一个结论要满足什么，才允许被采纳并写进生产代码"。
> 未来无论是人、还是 Agent 接手，都在同一套纪律下工作，不再重走已被证伪的路。

## 项目目标（正式修订）

以前：寻找 Alpha（提高方向预测准确率）。
现在：**最大化风险调整后收益**。指标优先级固定：

```
Primary    : CVaR95      （尾部风险，主指标）
Secondary  : Ulcer Index （回撤路径痛苦）
Third      : NetPnL      （短期是噪声，永不放第一）
```

一句话共识：**交易系统最大的可验证优势，不是提高预测能力，而是把有限的预测能力
转换成更合理的风险预算。**

---

## 十条纪律

1. **Random Control 必需**：任何 candidate 必须对比"效应量相当的随机对照"（如 notional-matched
   随机削减）。没有 Random Control 的结论不予采纳。p85 cap 之所以被接受，正因它的 CVaR 改善
   优于 100% 的等量随机削减——证明是"位置选对"，不是"少下注"。

2. **走查 H1→H2 必需**：全样本有效不算数，必须在时间前半/后半各自成立（至少不恶化主指标）。

3. **No Look-ahead（严格因果）**：任何多周期/对齐信号必须用 bar-CLOSE≤T，禁止偷看正在形成的 bar。
   历史教训：追涨因子与绿灯加仓的"edge"全是 loose alignment 偷看未收 bar 的前视假象。

4. **参数邻域必需**：不能只有某一个参数点最优。必须验证邻域（如 p80/p85/p90）都改善；
   只有单点好 = 过拟合嫌疑，驳回。

5. **主指标是 CVaR，不是 NetPnL**：在净亏账里，"缩小仓位"会天然把 NetPnL 推向 0 而显得"更好"，
   这是偏置。必须用 CVaR/Ulcer 作主判据，并识别出改善是否只是"缩仓副产品"。

6. **改善来源必须归类**：每篇研究第一页写明改善来自哪一类——
   **Information / Leverage / Execution / Cost / Risk**。
   例：p85 cap 属于 Leverage Allocation，不是 Information。否则几年后没人知道为什么有效。

7. **实验预登记（防 P-Hacking）**：跑之前先写 Experiment ID / Hypothesis / Primary Metric /
   Secondary Metric / Success Criteria。跑完不许改判据。100 个想法总会碰巧出几个 95%——
   预登记是唯一防线。

8. **Multiple Testing 意识**：做了几十次统计后，必须对"碰巧显著"保持警惕。跨多个假设时，
   提高显著性门槛或用 OOS/独立样本二次确认。

9. **负结果永久保留**：所有被证伪的想法进 Decision Log，附失败机制与"重开条件"。
   见 DecisionLog.md。

10. **OOS 收尾**：进生产前，尽量在样本外（不同时间段/币种）再验一次。零自由度规则（如固定 cap）
    可信度高；多自由度规则（如 vol-target 的 target+上限）必须 OOS 才算数。

---

## 改善来源分类（Improvement Source Taxonomy）

| 类别 | 定义 | 例子 |
|---|---|---|
| **Information** | 靠更好的信息/预测 | AI 方向判断（已证≈掷硬币，慎投） |
| **Leverage** | 靠更合理的仓位/风险预算，不依赖预测 | p85 notional cap、仓位价值比 |
| **Execution** | 靠更好的成交 | maker 入场、入场价偏离控制 |
| **Cost** | 靠降低费用 | maker 费、USDC 零费 |
| **Risk** | 靠削尾/限单笔风险 | time-stop、trailing、阶梯止盈、cap |

---

## 每篇研究的最小模板

```
Experiment ID   : EXP-YYYYMMDD-NN
Hypothesis      : （可证伪的一句话）
Improvement Src : Information / Leverage / Execution / Cost / Risk
Primary Metric  : CVaR95（默认）
Secondary       : Ulcer / NetPnL
Success Criteria : （跑之前写死，跑完不许改）
Controls        : Random Control? H1→H2? No-look-ahead? Param neighborhood?
Result          : 通过 / 驳回
→ 写入 DecisionLog.md
```
