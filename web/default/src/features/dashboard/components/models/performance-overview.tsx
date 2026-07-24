/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery } from '@tanstack/react-query'
import { Gauge, HeartPulse, Timer } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import {
  getPerfMetricsChannelSummary,
  getPerfMetricsSummary,
} from '@/features/performance-metrics/api'
import {
  formatLatency,
  formatThroughput,
  formatUptimePct,
  getSuccessRateLevel,
  getSuccessRateTextClass,
} from '@/features/performance-metrics/lib/format'
import type {
  PerfChannelSummary,
  PerfModelSummary,
} from '@/features/performance-metrics/types'
import { useIsAdmin } from '@/hooks/use-admin'
import { cn } from '@/lib/utils'

const PERFORMANCE_WINDOW_HOURS = 24
const TOP_MODEL_LIMIT = 6
const TOP_CHANNEL_LIMIT = 8

type PerfView = 'model' | 'channel'
const METRIC_SKELETON_KEYS = [
  'success-rate-skeleton',
  'latency-skeleton',
  'throughput-skeleton',
]

type WeightedMetric = 'avg_latency_ms' | 'avg_tps' | 'success_rate'

type PerformanceSummary = {
  totalRequests: number
  avgLatencyMs: number
  avgTps: number
  successRate: number
}

function simpleAverage(
  rows: PerfModelSummary[],
  metric: WeightedMetric,
  isValid: (value: number) => boolean
): number {
  let total = 0
  let count = 0

  for (const row of rows) {
    const value = Number(row[metric])
    if (!isValid(value)) continue
    total += value
    count++
  }

  return count > 0 ? total / count : Number.NaN
}

function buildPerformanceSummary(rows: PerfModelSummary[]): PerformanceSummary {
  return {
    totalRequests: rows.length,
    avgLatencyMs: Math.round(
      simpleAverage(
        rows,
        'avg_latency_ms',
        (value) => Number.isFinite(value) && value > 0
      )
    ),
    avgTps: simpleAverage(
      rows,
      'avg_tps',
      (value) => Number.isFinite(value) && value > 0
    ),
    successRate: simpleAverage(rows, 'success_rate', Number.isFinite),
  }
}

export function PerformanceOverview() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const [view, setView] = useState<PerfView>('model')
  const metricsQuery = useQuery({
    queryKey: ['perf-metrics-summary', PERFORMANCE_WINDOW_HOURS],
    queryFn: () => getPerfMetricsSummary(PERFORMANCE_WINDOW_HOURS),
    staleTime: 60 * 1000,
    retry: false,
  })

  // Per-channel breakdown is admin-only and fetched lazily when that view is active.
  const channelQuery = useQuery({
    queryKey: ['perf-metrics-channel-summary', PERFORMANCE_WINDOW_HOURS],
    queryFn: () => getPerfMetricsChannelSummary(PERFORMANCE_WINDOW_HOURS),
    staleTime: 60 * 1000,
    retry: false,
    enabled: isAdmin && view === 'channel',
  })

  const models = useMemo(
    () => metricsQuery.data?.data.models ?? [],
    [metricsQuery.data]
  )
  const channels = useMemo(
    () => channelQuery.data?.data.channels ?? [],
    [channelQuery.data]
  )
  const summary = useMemo(() => buildPerformanceSummary(models), [models])
  const topModels = useMemo(() => models.slice(0, TOP_MODEL_LIMIT), [models])
  const topChannels = useMemo(
    () => channels.slice(0, TOP_CHANNEL_LIMIT),
    [channels]
  )
  const channelView = view === 'channel'
  const loading = metricsQuery.isLoading
  const channelLoading = channelQuery.isLoading
  const hasData = models.length > 0

  if (!loading && !hasData) {
    return (
      <div className='text-muted-foreground overflow-hidden rounded-lg border px-4 py-3 text-center text-xs'>
        {t('No performance data available')}
      </div>
    )
  }

  return (
    <div className='overflow-hidden rounded-lg border'>
      <div className='flex flex-wrap items-center gap-x-5 gap-y-2.5 px-4 py-2.5 sm:px-5 sm:py-3'>
        {/* Title */}
        <div className='flex items-center gap-1.5'>
          <HeartPulse
            className='text-muted-foreground/60 size-3.5 shrink-0'
            aria-hidden='true'
          />
          <span className='text-xs font-semibold whitespace-nowrap'>
            {t('Performance health')}
          </span>
        </div>

        {/* Model / channel toggle (admin only) */}
        {isAdmin && (
          <ToggleGroup
            value={[view]}
            onValueChange={(value) => {
              const next = value.find((item) => item !== view)
              if (next === 'model' || next === 'channel') setView(next)
            }}
            aria-label={t('Performance breakdown')}
            variant='outline'
            size='sm'
            spacing={0}
          >
            <ToggleGroupItem value='model' className='px-2.5 text-xs'>
              {t('By model')}
            </ToggleGroupItem>
            <ToggleGroupItem value='channel' className='px-2.5 text-xs'>
              {t('By channel')}
            </ToggleGroupItem>
          </ToggleGroup>
        )}

        {/* Separator */}
        <div className='bg-border hidden h-4 w-px sm:block' />

        {/* 3 KPI inline metrics */}
        {loading ? (
          <div className='flex flex-wrap items-center gap-x-5 gap-y-2'>
            {METRIC_SKELETON_KEYS.map((key) => (
              <div key={key} className='flex items-center gap-1.5'>
                <Skeleton className='h-3 w-14' />
                <Skeleton className='h-4 w-16' />
              </div>
            ))}
          </div>
        ) : (
          <div className='flex flex-wrap items-center gap-x-5 gap-y-2'>
            <InlineMetric
              icon={HeartPulse}
              label={t('Success rate')}
              value={formatUptimePct(summary.successRate)}
              valueClassName={getSuccessRateTextClass(summary.successRate)}
            />
            <InlineMetric
              icon={Timer}
              label={t('Average latency')}
              value={formatLatency(summary.avgLatencyMs)}
            />
            <InlineMetric
              icon={Gauge}
              label={t('Throughput')}
              value={formatThroughput(summary.avgTps)}
            />
          </div>
        )}

        {/* Separator */}
        <div className='bg-border hidden h-4 w-px lg:block' />

        {/* Top models / channels inline badges */}
        {channelView ? (
          <ChannelBadges
            loading={channelLoading}
            channels={topChannels}
            emptyLabel={t('No channel performance data yet')}
          />
        ) : (
          !loading &&
          hasData && (
            <div className='flex flex-wrap items-center gap-1.5'>
              {topModels.map((model) => (
                <ModelBadge key={model.model_name} model={model} />
              ))}
            </div>
          )
        )}
      </div>
    </div>
  )
}

function ChannelBadges(props: {
  loading: boolean
  channels: PerfChannelSummary[]
  emptyLabel: string
}) {
  if (props.loading) {
    return (
      <div className='flex flex-wrap items-center gap-1.5'>
        {METRIC_SKELETON_KEYS.map((key) => (
          <Skeleton key={key} className='h-5 w-24 rounded-full' />
        ))}
      </div>
    )
  }
  if (props.channels.length === 0) {
    return (
      <span className='text-muted-foreground text-xs'>{props.emptyLabel}</span>
    )
  }
  return (
    <div className='flex flex-wrap items-center gap-1.5'>
      {props.channels.map((channel) => (
        <ChannelBadge key={channel.channel_id} channel={channel} />
      ))}
    </div>
  )
}

function InlineMetric(props: {
  icon: React.ComponentType<{ className?: string }>
  label: string
  value: string
  valueClassName?: string
}) {
  const Icon = props.icon

  return (
    <div className='flex items-center gap-1.5'>
      <Icon
        className='text-muted-foreground/50 size-3 shrink-0'
        aria-hidden='true'
      />
      <span className='text-muted-foreground text-xs'>{props.label}</span>
      <span
        className={cn(
          'text-xs font-semibold tabular-nums',
          props.valueClassName
        )}
      >
        {props.value}
      </span>
    </div>
  )
}

function successRateVariant(rate: number): StatusVariant {
  const level = getSuccessRateLevel(rate)
  if (level === 'excellent' || level === 'good') return 'success'
  if (level === 'warning') return 'warning'
  if (level === 'critical') return 'destructive'
  return 'neutral'
}

function ModelBadge(props: { model: PerfModelSummary }) {
  const model = props.model
  return (
    <StatusBadge variant={successRateVariant(model.success_rate)}>
      <span className='mr-1 max-w-[10rem] truncate'>{model.model_name}</span>
      <span className='tabular-nums'>
        {formatUptimePct(model.success_rate)}
      </span>
    </StatusBadge>
  )
}

function ChannelBadge(props: { channel: PerfChannelSummary }) {
  const { t } = useTranslation()
  const channel = props.channel
  // channel_id = 0 is the sentinel for historical failures that predate
  // per-channel recording and cannot be attributed to a real channel.
  const label =
    channel.channel_id === 0
      ? t('Unattributed (historical)')
      : channel.channel_name || `#${channel.channel_id}`

  return (
    <StatusBadge variant={successRateVariant(channel.success_rate)}>
      <span className='mr-1 max-w-[10rem] truncate'>{label}</span>
      <span className='tabular-nums'>
        {formatUptimePct(channel.success_rate)}
      </span>
    </StatusBadge>
  )
}
