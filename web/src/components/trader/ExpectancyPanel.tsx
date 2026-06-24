import type { Statistics } from '../../types/trading'

interface ExpectancyPanelProps {
  stats?: Statistics
  language: string
}

const L = (lang: string, zh: string, en: string) => (lang === 'zh' ? zh : en)

// ExpectancyPanel surfaces the single most important strategy-health question:
// does the trader have a positive per-trade edge after fees? Driven by the
// /statistics expectancy fields (net of fees), computed from closed positions.
export function ExpectancyPanel({ stats, language }: ExpectancyPanelProps) {
  if (!stats || !stats.closed_trades) return null

  const flag = stats.health_flag || 'marginal'
  const flagColor =
    flag === 'positive'
      ? 'text-green-400'
      : flag === 'negative'
        ? 'text-red-400'
        : 'text-yellow-400'
  const flagBg =
    flag === 'positive'
      ? 'bg-green-500/10 border-green-500/30'
      : flag === 'negative'
        ? 'bg-red-500/10 border-red-500/30'
        : 'bg-yellow-500/10 border-yellow-500/30'
  const flagLabel =
    flag === 'positive'
      ? L(language, '正期望', 'Positive edge')
      : flag === 'negative'
        ? L(language, '负期望', 'Negative edge')
        : L(language, '临界', 'Marginal')

  const expectancy = stats.expectancy_usd ?? 0
  const payoff = stats.payoff_ratio ?? 0
  const netWinRate = (stats.net_win_rate ?? 0) * 100
  const feeDrag = stats.fee_drag_ratio ?? 0
  const netPnl = stats.net_pnl_usd ?? 0
  const fees = stats.total_fees_usd ?? 0

  // Payoff and win-rate context coloring: payoff>1 good; feeDrag>1 is a red flag.
  const payoffColor = payoff >= 1 ? 'text-green-400' : 'text-red-400'
  const feeColor =
    feeDrag >= 1
      ? 'text-red-400'
      : feeDrag >= 0.5
        ? 'text-yellow-400'
        : 'text-zinc-300'

  return (
    <div
      className={`mb-4 rounded-lg border p-4 animate-slide-in ${flagBg}`}
      style={{ animationDelay: '0.06s' }}
    >
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-zinc-200">
            {L(language, '策略期望值', 'Strategy Expectancy')}
          </span>
          <span
            className="text-xs text-zinc-500"
            title={L(
              language,
              '单笔期望 = 净盈亏 / 已平仓笔数（已扣手续费）。<0 表示长期亏损。',
              'Expectancy = net PnL / closed trades (fees included). <0 = loses over time.'
            )}
          >
            ⓘ
          </span>
        </div>
        <span
          className={`text-xs font-semibold px-2 py-0.5 rounded ${flagColor}`}
        >
          {flagLabel}
        </span>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-6 gap-3 text-center">
        <Metric
          label={L(language, '单笔期望', 'Expectancy')}
          value={`${expectancy >= 0 ? '+' : ''}${expectancy.toFixed(3)}`}
          unit="USDT"
          color={expectancy >= 0 ? 'text-green-400' : 'text-red-400'}
        />
        <Metric
          label={L(language, '盈亏比', 'Payoff')}
          value={payoff.toFixed(2)}
          unit="x"
          color={payoffColor}
        />
        <Metric
          label={L(language, '净胜率', 'Net win rate')}
          value={netWinRate.toFixed(1)}
          unit="%"
        />
        <Metric
          label={L(language, '净盈亏', 'Net PnL')}
          value={`${netPnl >= 0 ? '+' : ''}${netPnl.toFixed(2)}`}
          unit="USDT"
          color={netPnl >= 0 ? 'text-green-400' : 'text-red-400'}
        />
        <Metric
          label={L(language, '手续费占比', 'Fee drag')}
          value={feeDrag.toFixed(2)}
          unit="x"
          color={feeColor}
        />
        <Metric
          label={L(language, '累计手续费', 'Total fees')}
          value={fees.toFixed(2)}
          unit="USDT"
          color="text-zinc-400"
        />
      </div>

      {flag === 'negative' && (
        <div className="mt-3 text-xs text-red-300/80">
          {feeDrag >= 1
            ? L(
                language,
                '⚠️ 手续费已超过毛盈亏，过度交易在吞噬本金。优先降低换手 / 启用挂单进场。',
                '⚠️ Fees exceed gross PnL — overtrading is eroding capital. Reduce turnover / enable maker entry.'
              )
            : payoff < 1
              ? L(
                  language,
                  '⚠️ 盈亏比 < 1：平均亏损大于平均盈利。建议启用移动止盈、提高 R:R 门槛。',
                  '⚠️ Payoff < 1: average loss exceeds average win. Enable trailing TP and raise the R:R gate.'
                )
              : L(
                  language,
                  '⚠️ 单笔期望为负，长期会亏损。检查胜率与盈亏比结构。',
                  '⚠️ Negative per-trade expectancy. Review win-rate and payoff structure.'
                )}
        </div>
      )}
    </div>
  )
}

function Metric({
  label,
  value,
  unit,
  color = 'text-zinc-200',
}: {
  label: string
  value: string
  unit?: string
  color?: string
}) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] uppercase tracking-wide text-zinc-500">
        {label}
      </span>
      <span className={`text-base font-semibold ${color}`}>
        {value}
        {unit && (
          <span className="text-[10px] text-zinc-500 ml-0.5">{unit}</span>
        )}
      </span>
    </div>
  )
}
