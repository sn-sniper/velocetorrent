import React from 'react';
import type { StreamConnectionState } from '../../hooks/useTelemetryStream';
import { formatBytes, formatRate } from '../../lib/formatters';
import type { EngineTelemetrySnapshot, VelocitySample } from '../../types/telemetry';
import { Button } from '../ui/Button';

interface TopTelemetryBarProps {
  telemetry: EngineTelemetrySnapshot | null;
  connectionState: StreamConnectionState;
  onOpenDirManager: () => void;
  onSpawnTestSwarm: () => void;
  onToggleThrottle: () => void;
  isSpawningSwarm: boolean;
}

const SvgVelocitySparkline: React.FC<{
  samples: VelocitySample[];
  field: 'downBytesPs' | 'upBytesPs';
  strokeColor: string;
  fillColor: string;
}> = ({ samples, field, strokeColor, fillColor }) => {
  const width = 180;
  const height = 28;
  const values = samples.length > 0 ? samples.map((s) => s[field]) : [0, 0];
  const maxVal = Math.max(1024, ...values);

  const points = values
    .map((v, i) => {
      const x = (i / Math.max(1, values.length - 1)) * width;
      const y = height - 2 - (v / maxVal) * (height - 4);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');

  const areaPoints = `0,${height} ${points} ${width},${height}`;

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className="bg-zinc-950 border border-zinc-800 shrink-0"
      aria-label={`${field} 60s sparkline`}
    >
      <polygon points={areaPoints} fill={fillColor} />
      <polyline
        fill="none"
        stroke={strokeColor}
        strokeWidth="1.5"
        points={points}
      />
    </svg>
  );
};

export const TopTelemetryBar: React.FC<TopTelemetryBarProps> = ({
  telemetry,
  connectionState,
  onOpenDirManager,
  onSpawnTestSwarm,
  onToggleThrottle,
  isSpawningSwarm,
}) => {
  const downRate = telemetry?.globalDownBytesPs ?? 0;
  const upRate = telemetry?.globalUpBytesPs ?? 0;
  const activePeers = telemetry?.globalActivePeers ?? 0;
  const swarmPeers = telemetry?.globalSwarmPeers ?? 0;
  const diskQueue = telemetry?.globalDiskQueue ?? 0;
  const hashQueue = telemetry?.globalHashQueue ?? 0;
  const iops = telemetry?.globalIOPS ?? 0;
  const freeBytes = telemetry?.diskFreeBytes ?? 0;
  const defaultDir = telemetry?.defaultOutputDir ?? '—';
  const history = telemetry?.velocityHistory ?? [];
  const throttleProfile = telemetry?.throttle?.profile ?? 'UNCAPPED';

  const connDotColor =
    connectionState === 'LIVE'
      ? 'bg-emerald-500'
      : connectionState === 'CONNECTING' || connectionState === 'RECONNECTING'
      ? 'bg-amber-500'
      : 'bg-red-500';

  return (
    <header className="w-full bg-zinc-900 border-b border-zinc-800">
      <div className="max-w-[1600px] mx-auto px-4 py-2.5 flex flex-wrap items-center justify-between gap-4">
        {/* Identity & Live WS Status */}
        <div className="flex items-center gap-2.5">
          <div className="flex items-center gap-2 border border-zinc-800 bg-zinc-950 px-2.5 py-1">
            <span className={`w-2 h-2 ${connDotColor}`} />
            <span className="font-mono text-xs font-bold tracking-wider text-zinc-100">
              VELOCETORRENT
            </span>
            <span className="font-mono text-[10px] text-zinc-500">
              {connectionState === 'LIVE' ? '100MS IPC' : connectionState}
            </span>
          </div>

          <Button
            variant={throttleProfile === 'UNCAPPED' ? 'secondary' : 'primary'}
            size="xs"
            onClick={onToggleThrottle}
            title="Toggle Bandwidth Scheduler between Uncapped Line-Rate and Background (2 MiB/s) Mode"
          >
            {throttleProfile === 'UNCAPPED' ? 'BW: UNCAPPED' : 'BW: BACKGROUND'}
          </Button>

          <Button
            variant="secondary"
            size="xs"
            onClick={onSpawnTestSwarm}
            disabled={isSpawningSwarm}
            title="Spawn a real multi-piece TCP loopback BitTorrent swarm transfer"
          >
            {isSpawningSwarm ? 'STARTING SWARM...' : '+ TEST SWARM'}
          </Button>
        </div>

        {/* High-Frequency Telemetry Readouts & 60s Custom SVG Sparklines */}
        <div className="flex flex-wrap items-center gap-4 font-mono tabular-nums">
          <div className="flex items-center gap-2">
            <div className="flex flex-col text-right">
              <span className="text-[10px] text-zinc-500 uppercase">DOWN</span>
              <span className="text-xs font-semibold text-emerald-400 min-w-21">
                {formatRate(downRate)}
              </span>
            </div>
            <SvgVelocitySparkline
              samples={history}
              field="downBytesPs"
              strokeColor="#10B981"
              fillColor="rgba(16, 185, 129, 0.12)"
            />
          </div>

          <div className="flex items-center gap-2">
            <div className="flex flex-col text-right">
              <span className="text-[10px] text-zinc-500 uppercase">UP</span>
              <span className="text-xs font-semibold text-zinc-200 min-w-21">
                {formatRate(upRate)}
              </span>
            </div>
            <SvgVelocitySparkline
              samples={history}
              field="upBytesPs"
              strokeColor="#A1A1AA"
              fillColor="rgba(161, 161, 170, 0.08)"
            />
          </div>

          <div className="h-7 w-px bg-zinc-800 hidden md:block" />

          <div className="grid grid-cols-4 gap-4 text-xs">
            <div className="flex flex-col">
              <span className="text-[10px] text-zinc-500">SWARM</span>
              <span className="text-zinc-200 font-medium">
                {activePeers}/{swarmPeers}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-[10px] text-zinc-500">IOPS</span>
              <span className="text-zinc-200 font-medium">{iops}</span>
            </div>
            <div className="flex flex-col">
              <span className="text-[10px] text-zinc-500">QUEUE</span>
              <span className="text-zinc-200 font-medium">
                D:{diskQueue} H:{hashQueue}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-[10px] text-zinc-500">FREE</span>
              <span className="text-zinc-200 font-medium">
                {formatBytes(freeBytes)}
              </span>
            </div>
          </div>

          <div className="h-7 w-px bg-zinc-800 hidden lg:block" />

          {/* Default Output Directory Quick-Trigger */}
          <div className="flex items-center gap-2 bg-zinc-950 border border-zinc-800 px-2.5 py-1">
            <span className="text-[10px] text-zinc-500">DIR</span>
            <span
              className="text-xs text-zinc-300 max-w-55 truncate"
              title={defaultDir}
            >
              {defaultDir}
            </span>
            <Button variant="ghost" size="xs" onClick={onOpenDirManager}>
              CHANGE
            </Button>
          </div>
        </div>
      </div>
    </header>
  );
};
