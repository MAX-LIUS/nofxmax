import { useState, useCallback } from 'react'
import type { DecisionRecord, DecisionAction } from '../../types'
import { type Language } from '../../i18n/translations'
import { api } from '../../lib/api'

interface DecisionCardProps {
  decision: DecisionRecord
  language: Language
  traderId?: string
  onSymbolClick?: (symbol: string) => void
}

const ACTION_CONFIG: Record<
  string,
  { color: string; bg: string; icon: string; label: string }
> = {
  open_long: {
    color: '#0ECB81',
    bg: 'rgba(14,203,129,0.15)',
    icon: '📈',
    label: 'LONG',
  },
  open_short: {
    color: '#F6465D',
    bg: 'rgba(246,70,93,0.15)',
    icon: '📉',
    label: 'SHORT',
  },
  close_long: {
    color: '#F0B90B',
    bg: 'rgba(240,185,11,0.15)',
    icon: '💰',
    label: 'CLOSE',
  },
  close_short: {
    color: '#F0B90B',
    bg: 'rgba(240,185,11,0.15)',
    icon: '💰',
    label: 'CLOSE',
  },
  hold: {
    color: '#848E9C',
    bg: 'rgba(132,142,156,0.15)',
    icon: '⏸️',
    label: 'HOLD',
  },
  wait: {
    color: '#848E9C',
    bg: 'rgba(132,142,156,0.15)',
    icon: '⏳',
    label: 'WAIT',
  },
}

function formatPrice(price: number | undefined): string {
  if (!price || price === 0) return '-'
  if (price >= 1000) return price.toFixed(2)
  if (price >= 1) return price.toFixed(4)
  return price.toFixed(6)
}

function formatUsd(v: number | undefined): string {
  if (!v || v === 0) return '-'
  if (v >= 1000) return `$${(v / 1000).toFixed(1)}k`
  return `$${v.toFixed(0)}`
}

function getConfidenceColor(c: number | undefined): string {
  if (!c) return '#848E9C'
  if (c >= 80) return '#0ECB81'
  if (c >= 60) return '#F0B90B'
  return '#F6465D'
}

// Classify an action's control/gate outcome -> visual tone for row tinting.
type Tone = 'accepted' | 'rejected' | 'neutral'
function actionTone(action: DecisionAction): Tone {
  const ctl = String(
    action.review_context?.control?.decision || ''
  ).toLowerCase()
  const gate = String(
    action.review_context?.quality_gate?.decision || ''
  ).toLowerCase()
  if (ctl === 'rejected' || gate === 'rejected' || gate === 'blocked')
    return 'rejected'
  if (ctl === 'downgraded_to_wait') return 'rejected'
  if (action.success && action.action.includes('open')) return 'accepted'
  if (
    ctl.startsWith('accepted') ||
    gate === 'passed' ||
    gate.startsWith('accepted')
  )
    return 'accepted'
  return 'neutral'
}

// TONE_STYLE gives the row background/border per the user's spec:
// rejected = dimmer, reddish; accepted = slightly brighter, greenish.
const TONE_STYLE: Record<Tone, { bg: string; border: string }> = {
  rejected: {
    bg: 'rgba(246,70,93,0.06)',
    border: '1px solid rgba(246,70,93,0.22)',
  },
  accepted: {
    bg: 'rgba(14,203,129,0.09)',
    border: '1px solid rgba(14,203,129,0.30)',
  },
  neutral: { bg: '#1A1E23', border: '1px solid #2B3139' },
}

// ---- ActionCard: one compact line per decision action ----------------------
// Layout (single row, wraps on narrow/portrait): symbol · direction · entry
// target (actual entry below or "-") · SL · TP · leverage · notional USDT ·
// confidence · RR · score (expandable breakdown).
function ActionCard({
  action,
  onSymbolClick,
}: {
  action: DecisionAction
  onSymbolClick?: (symbol: string) => void
}) {
  const [showScore, setShowScore] = useState(false)
  const cfg = ACTION_CONFIG[action.action] || ACTION_CONFIG.hold
  const tone = actionTone(action)
  const toneStyle = TONE_STYLE[tone]
  const isTrade =
    action.action.includes('open') || action.action.includes('close')

  const rc = action.review_context
  const rr = rc?.risk_reward
  const ctl = rc?.control
  const gate = rc?.quality_gate
  const levels = rc?.selected_levels || []

  // entry trigger target: prefer selected_levels entry_trigger, fall back to price.
  const entryTrigger =
    levels.find((l) => l.used_for === 'entry_trigger')?.price ?? action.price
  // actual entry (filled) — only known when the order succeeded; otherwise "-".
  const actualEntry = action.success && action.price ? action.price : undefined

  const notional =
    action.price && action.quantity ? action.price * action.quantity : undefined
  const netRr = rr?.net_estimated_rr ?? ctl?.effective_rr ?? gate?.net_rr
  const scoreTotal = gate?.quality_total

  // Compact "hold / wait" rows: no trade fields, just the tag + reason.
  if (!isTrade) {
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          padding: '6px 10px',
          background: toneStyle.bg,
          border: toneStyle.border,
          borderRadius: 6,
          fontSize: 12,
        }}
      >
        <span style={{ color: cfg.color, fontWeight: 600 }}>
          {cfg.icon} {cfg.label}
        </span>
        <span
          onClick={() => onSymbolClick?.(action.symbol)}
          style={{
            color: '#EAECEF',
            fontWeight: 600,
            cursor: onSymbolClick ? 'pointer' : 'default',
          }}
        >
          {action.symbol}
        </span>
        {action.reasoning && (
          <span
            style={{
              color: '#848E9C',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {action.reasoning}
          </span>
        )}
      </div>
    )
  }

  // Metric cell used inside the flex-wrap grid.
  const Cell = ({
    label,
    value,
    color,
    sub,
  }: {
    label: string
    value: string
    color?: string
    sub?: string
  }) => (
    <div style={{ display: 'flex', flexDirection: 'column', minWidth: 62 }}>
      <span
        style={{
          fontSize: 9,
          color: '#5E6673',
          textTransform: 'uppercase',
          letterSpacing: 0.3,
        }}
      >
        {label}
      </span>
      <span
        style={{
          fontSize: 12,
          color: color || '#EAECEF',
          fontWeight: 600,
          fontVariantNumeric: 'tabular-nums',
        }}
      >
        {value}
      </span>
      {sub !== undefined && (
        <span
          style={{
            fontSize: 10,
            color: '#5E6673',
            fontVariantNumeric: 'tabular-nums',
          }}
        >
          {sub}
        </span>
      )}
    </div>
  )

  return (
    <div
      style={{
        padding: '8px 10px',
        background: toneStyle.bg,
        border: toneStyle.border,
        borderRadius: 6,
      }}
    >
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          flexWrap: 'wrap',
        }}
      >
        {/* direction + symbol */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            minWidth: 108,
          }}
        >
          <span
            style={{
              color: cfg.color,
              background: cfg.bg,
              fontWeight: 700,
              fontSize: 11,
              padding: '2px 6px',
              borderRadius: 4,
              whiteSpace: 'nowrap',
            }}
          >
            {cfg.icon} {cfg.label}
          </span>
          <span
            onClick={() => onSymbolClick?.(action.symbol)}
            style={{
              color: '#EAECEF',
              fontWeight: 700,
              fontSize: 13,
              cursor: onSymbolClick ? 'pointer' : 'default',
            }}
          >
            {action.symbol.replace('USDT', '').replace('USDC', '')}
          </span>
        </div>

        <Cell
          label="Entry"
          value={formatPrice(entryTrigger)}
          sub={actualEntry ? formatPrice(actualEntry) : '-'}
        />
        <Cell
          label="SL"
          value={formatPrice(action.stop_loss)}
          color="#F6465D"
        />
        <Cell
          label="TP"
          value={formatPrice(action.take_profit)}
          color="#0ECB81"
        />
        <Cell
          label="Lev"
          value={action.leverage ? `${action.leverage}x` : '-'}
        />
        <Cell label="Size" value={formatUsd(notional)} />
        <Cell
          label="Conf"
          value={action.confidence != null ? `${action.confidence}%` : '-'}
          color={getConfidenceColor(action.confidence)}
        />
        <Cell label="RR" value={netRr != null ? netRr.toFixed(2) : '-'} />
        {scoreTotal != null && (
          <div
            onClick={() => setShowScore((v) => !v)}
            style={{
              display: 'flex',
              flexDirection: 'column',
              minWidth: 56,
              cursor: 'pointer',
            }}
          >
            <span
              style={{
                fontSize: 9,
                color: '#5E6673',
                textTransform: 'uppercase',
              }}
            >
              Score {showScore ? '▲' : '▼'}
            </span>
            <span style={{ fontSize: 12, color: '#F0B90B', fontWeight: 600 }}>
              {scoreTotal.toFixed(0)}
            </span>
          </div>
        )}
      </div>

      {/* score breakdown dropdown */}
      {showScore && gate && (
        <div
          style={{
            marginTop: 8,
            padding: 8,
            background: '#12151A',
            borderRadius: 4,
            fontSize: 11,
            color: '#B7BDC6',
          }}
        >
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: 'auto 1fr',
              gap: '2px 10px',
            }}
          >
            {gate.setup_type && (
              <>
                <span style={{ color: '#5E6673' }}>Setup</span>
                <span>{gate.setup_type}</span>
              </>
            )}
            {gate.regime && (
              <>
                <span style={{ color: '#5E6673' }}>Regime</span>
                <span>{gate.regime}</span>
              </>
            )}
            {gate.confidence != null && (
              <>
                <span style={{ color: '#5E6673' }}>Conf</span>
                <span>{gate.confidence}</span>
              </>
            )}
            {gate.net_rr != null && (
              <>
                <span style={{ color: '#5E6673' }}>Net RR</span>
                <span>{gate.net_rr.toFixed(2)}</span>
              </>
            )}
            {gate.decision && (
              <>
                <span style={{ color: '#5E6673' }}>Gate</span>
                <span>{gate.decision}</span>
              </>
            )}
          </div>
          {gate.failed_checks && gate.failed_checks.length > 0 && (
            <div style={{ marginTop: 6, color: '#F6465D' }}>
              ✗ {gate.failed_checks.join(', ')}
            </div>
          )}
          {gate.gate_checks && gate.gate_checks.length > 0 && (
            <div
              style={{
                marginTop: 6,
                display: 'flex',
                flexDirection: 'column',
                gap: 2,
              }}
            >
              {gate.gate_checks.map((gc, i) => (
                <div
                  key={i}
                  style={{ color: gc.passed ? '#0ECB81' : '#F6465D' }}
                >
                  {gc.passed ? '✓' : '✗'} {gc.code}{' '}
                  {gc.detail ? `— ${gc.detail}` : ''}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {action.error && (
        <div style={{ marginTop: 6, fontSize: 11, color: '#F6465D' }}>
          ⚠️ {action.error}
        </div>
      )}
    </div>
  )
}

// ---- Collapsible that lazy-fetches its content on first expand -------------
function Collapsible({
  title,
  color,
  defaultOpen,
  children,
  onFirstOpen,
}: {
  title: string
  color?: string
  defaultOpen?: boolean
  children: React.ReactNode
  onFirstOpen?: () => void
}) {
  const [open, setOpen] = useState(!!defaultOpen)
  const [everOpened, setEverOpened] = useState(!!defaultOpen)
  const toggle = () => {
    const next = !open
    setOpen(next)
    if (next && !everOpened) {
      setEverOpened(true)
      onFirstOpen?.()
    }
  }
  return (
    <div style={{ borderTop: '1px solid #2B3139' }}>
      <button
        onClick={toggle}
        style={{
          width: '100%',
          textAlign: 'left',
          background: 'transparent',
          border: 'none',
          color: color || '#848E9C',
          fontSize: 11,
          fontWeight: 600,
          padding: '6px 0',
          cursor: 'pointer',
          display: 'flex',
          alignItems: 'center',
          gap: 6,
        }}
      >
        <span>{open ? '▼' : '▶'}</span>
        {title}
      </button>
      {open && everOpened && <div style={{ paddingBottom: 8 }}>{children}</div>}
    </div>
  )
}

function PromptBlock({ text, loading }: { text: string; loading: boolean }) {
  if (loading)
    return (
      <div style={{ fontSize: 11, color: '#5E6673', padding: 8 }}>Loading…</div>
    )
  if (!text)
    return <div style={{ fontSize: 11, color: '#5E6673', padding: 8 }}>—</div>
  return (
    <pre
      style={{
        margin: 0,
        maxHeight: 320,
        overflow: 'auto',
        background: '#0B0E11',
        border: '1px solid #2B3139',
        borderRadius: 4,
        padding: 8,
        fontSize: 11,
        color: '#B7BDC6',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-word',
      }}
    >
      {text}
    </pre>
  )
}

// ---- Main card -------------------------------------------------------------
export function DecisionCard({
  decision,
  traderId,
  onSymbolClick,
}: DecisionCardProps) {
  const [expanded, setExpanded] = useState(false)
  const [prompts, setPrompts] = useState<{
    system_prompt: string
    input_prompt: string
    cot_trace: string
  } | null>(null)
  const [loadingPrompts, setLoadingPrompts] = useState(false)

  const loadPrompts = useCallback(async () => {
    if (prompts || loadingPrompts || !traderId) return
    setLoadingPrompts(true)
    try {
      const p = await api.getDecisionPrompts(traderId, decision.cycle_number)
      setPrompts({
        system_prompt: p.system_prompt,
        input_prompt: p.input_prompt,
        cot_trace: p.cot_trace,
      })
    } catch {
      setPrompts({ system_prompt: '', input_prompt: '', cot_trace: '' })
    } finally {
      setLoadingPrompts(false)
    }
  }, [prompts, loadingPrompts, traderId, decision.cycle_number])

  const actions = decision.decisions || []
  const ts = new Date(decision.timestamp)
  const mode = decision.ai_decision_mode || 'balanced'
  const modeColor =
    mode === 'aggressive'
      ? '#F6465D'
      : mode === 'conservative'
        ? '#0ECB81'
        : '#F0B90B'

  // AI control snapshot flags — shown expanded in the header row.
  const flag = (on: boolean | undefined) => (on ? 'ON' : 'OFF')
  const flagColor = (on: boolean | undefined) => (on ? '#0ECB81' : '#5E6673')

  return (
    <div
      style={{
        background: '#161A1E',
        border: `1px solid ${decision.success ? '#2B3139' : 'rgba(246,70,93,0.4)'}`,
        borderRadius: 8,
        padding: 12,
        marginBottom: 10,
      }}
    >
      {/* header: cycle # + timestamp + AI mode (+ control snapshot when expanded) */}
      <div
        onClick={() => setExpanded((v) => !v)}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          cursor: 'pointer',
          flexWrap: 'wrap',
        }}
      >
        <span style={{ fontSize: 12 }}>{expanded ? '▼' : '▶'}</span>
        <span style={{ fontWeight: 700, color: '#EAECEF', fontSize: 13 }}>
          🤖 #{decision.cycle_number}
        </span>
        <span style={{ fontSize: 11, color: '#848E9C' }}>
          {ts.toLocaleString()}
        </span>
        <span
          style={{
            fontSize: 10,
            fontWeight: 700,
            color: modeColor,
            border: `1px solid ${modeColor}`,
            borderRadius: 4,
            padding: '1px 6px',
            textTransform: 'uppercase',
          }}
        >
          {mode}
        </span>
        {!decision.success && (
          <span style={{ fontSize: 10, color: '#F6465D', fontWeight: 700 }}>
            FAILED
          </span>
        )}
        {expanded && (
          <span
            style={{
              display: 'flex',
              gap: 10,
              fontSize: 10,
              marginLeft: 'auto',
            }}
          >
            <span style={{ color: flagColor(decision.allow_ai_open) }}>
              Open:{flag(decision.allow_ai_open)}
            </span>
            <span style={{ color: flagColor(decision.allow_ai_stop_close) }}>
              SL:{flag(decision.allow_ai_stop_close)}
            </span>
            <span style={{ color: flagColor(decision.allow_ai_take_profit) }}>
              TP:{flag(decision.allow_ai_take_profit)}
            </span>
          </span>
        )}
      </div>

      {/* actions: always show a compact summary of trade actions */}
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          gap: 6,
          marginTop: 10,
        }}
      >
        {actions.length === 0 ? (
          <div style={{ fontSize: 11, color: '#5E6673' }}>No actions</div>
        ) : (
          actions.map((a, i) => (
            <ActionCard key={i} action={a} onSymbolClick={onSymbolClick} />
          ))
        )}
      </div>

      {expanded && (
        <div style={{ marginTop: 10 }}>
          {/* execution log */}
          {decision.execution_log && decision.execution_log.length > 0 && (
            <Collapsible title={`📋 $Execution Log`} defaultOpen>
              <div
                style={{
                  background: '#0B0E11',
                  border: '1px solid #2B3139',
                  borderRadius: 4,
                  padding: 8,
                  maxHeight: 240,
                  overflow: 'auto',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 2,
                }}
              >
                {decision.execution_log.map((line, i) => (
                  <div
                    key={i}
                    style={{
                      fontSize: 11,
                      fontFamily: 'monospace',
                      color:
                        line.includes('🚫') ||
                        line.includes('blocked') ||
                        line.includes('failed')
                          ? '#F6465D'
                          : line.includes('✓') || line.includes('succeeded')
                            ? '#0ECB81'
                            : '#B7BDC6',
                      whiteSpace: 'pre-wrap',
                      wordBreak: 'break-word',
                    }}
                  >
                    {line}
                  </div>
                ))}
              </div>
            </Collapsible>
          )}

          {decision.error_message && (
            <div
              style={{
                marginTop: 8,
                padding: 8,
                background: 'rgba(246,70,93,0.1)',
                border: '1px solid rgba(246,70,93,0.3)',
                borderRadius: 4,
                fontSize: 11,
                color: '#F6465D',
              }}
            >
              ⚠️ {decision.error_message}
            </div>
          )}

          {/* lazy-loaded prompts / CoT */}
          <Collapsible title="🧠 AI Thinking (CoT)" onFirstOpen={loadPrompts}>
            <PromptBlock
              text={prompts?.cot_trace || ''}
              loading={loadingPrompts}
            />
          </Collapsible>
          <Collapsible title="⚙️ System Prompt" onFirstOpen={loadPrompts}>
            <PromptBlock
              text={prompts?.system_prompt || ''}
              loading={loadingPrompts}
            />
          </Collapsible>
          <Collapsible title="💬 User Prompt" onFirstOpen={loadPrompts}>
            <PromptBlock
              text={prompts?.input_prompt || ''}
              loading={loadingPrompts}
            />
          </Collapsible>
        </div>
      )}
    </div>
  )
}
