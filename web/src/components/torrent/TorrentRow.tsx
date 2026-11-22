import React, { useEffect, useRef, useState } from 'react';
import { formatBytes, formatETA, formatPercent, formatRate } from '../../lib/formatters';
import type { FilePriority, TorrentTelemetrySnapshot } from '../../types/telemetry';
import { StatusBadge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { PieceMap } from './PieceMap';

interface TorrentRowProps {
  torrent: TorrentTelemetrySnapshot;
  savedDirs: string[];
  isExpanded: boolean;
  onToggleExpand: () => void;
  onTogglePause: (id: string) => void;
  onStop: (id: string) => void;
  onRelocateDir: (id: string, newDir: string) => Promise<unknown>;
  onSetFilePriority: (id: string, fileIndex: number, priority: FilePriority) => Promise<unknown>;
  onToggleStreamMode: (id: string, enabled: boolean) => Promise<unknown>;
  onRemove: (id: string, deleteFiles: boolean) => void;
}

const RowVelocitySparkline: React.FC<{ history: number[]; active: boolean }> = ({
  history,
  active,
}) => {
  const width = 76;
  const height = 20;
  const values = history.length > 1 ? history : [0, 0];
  const maxVal = Math.max(1024, ...values);

  const points = values
    .map((v, i) => {
      const x = (i / Math.max(1, values.length - 1)) * width;
      const y = height - 2 - (v / maxVal) * (height - 4);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className="bg-zinc-950 border border-zinc-800 shrink-0"
      aria-label="Torrent throughput sparkline"
    >
      <polyline
        fill="none"
        stroke={active ? '#10B981' : '#52525B'}
        strokeWidth="1.25"
        points={points}
      />
    </svg>
  );
};

export const TorrentRow: React.FC<TorrentRowProps> = ({
  torrent,
  savedDirs,
  isExpanded,
  onToggleExpand,
  onTogglePause,
  onStop,
  onRelocateDir,
  onSetFilePriority,
  onToggleStreamMode,
  onRemove,
}) => {
  const [inspectorTab, setInspectorTab] = useState<'PIECES' | 'PEERS' | 'FILES' | 'DIR'>('PIECES');
  const [newDirInput, setNewDirInput] = useState(torrent.outputDir);
  const [isRelocating, setIsRelocating] = useState(false);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [deleteFilesFromDisk, setDeleteFilesFromDisk] = useState(false);

  // Rolling 60-sample buffer for this torrent's custom <svg> velocity sparkline
  const rateHistoryRef = useRef<number[]>(new Array(30).fill(0));
  useEffect(() => {
    const next = [...rateHistoryRef.current.slice(-59), torrent.downBytesPs];
    rateHistoryRef.current = next;
  }, [torrent.downBytesPs, torrent.verifiedBytes]);

  const isPaused = torrent.status === 'PAUSED' || torrent.status === 'STOPPED';
  const isStopped = torrent.status === 'STOPPED';
  const barColor =
    torrent.status === 'DOWNLOADING' ||
    torrent.status === 'SEEDING' ||
    torrent.status === 'COMPLETED'
      ? 'bg-emerald-500'
      : torrent.status === 'METADATA' ||
        torrent.status === 'VERIFYING' ||
        torrent.status === 'PREALLOCATING'
      ? 'bg-amber-500'
      : torrent.status === 'ERROR'
      ? 'bg-red-500'
      : 'bg-zinc-600';

  const handleRelocateSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newDirInput.trim()) return;
    setIsRelocating(true);
    try {
      await onRelocateDir(torrent.id, newDirInput.trim());
    } finally {
      setIsRelocating(false);
    }
  };

  return (
    <div className="border-b border-zinc-800 last:border-b-0 bg-zinc-900/60 hover:bg-zinc-900 transition-colors font-mono tabular-nums">
      {/* Main Tabular Telemetry Row */}
      <div className="grid grid-cols-12 items-center gap-2 px-3 py-2.5 text-xs">
        {/* Name & InfoHash */}
        <div
          className="col-span-12 lg:col-span-2 flex flex-col gap-0.5 cursor-pointer"
          onClick={onToggleExpand}
        >
          <div className="flex items-center gap-2">
            <span className="text-zinc-500 text-[10px]">{isExpanded ? '▼' : '▸'}</span>
            <span className="font-semibold text-zinc-100 truncate" title={torrent.name}>
              {torrent.name}
            </span>
          </div>
          <div className="flex items-center gap-2 pl-4 text-[10px] text-zinc-500 truncate">
            <span>HASH:{torrent.infoHash.slice(0, 10)}</span>
            <span>•</span>
            <span className="truncate" title={torrent.outputDir}>
              DIR:{torrent.outputDir}
            </span>
          </div>
        </div>

        {/* Status Badge */}
        <div className="col-span-3 lg:col-span-1">
          <StatusBadge status={torrent.status} />
        </div>

        {/* High-Precision Progress Bar + Percentage */}
        <div className="col-span-9 lg:col-span-2 flex items-center gap-2">
          <div className="w-full h-2 bg-zinc-950 border border-zinc-800 overflow-hidden">
            <div
              className={`h-full transition-all duration-100 ${barColor}`}
              style={{ width: `${Math.min(100, Math.max(0, torrent.progress))}%` }}
            />
          </div>
          <span className="text-zinc-200 w-14 text-right">
            {formatPercent(torrent.progress)}
          </span>
        </div>

        {/* Verified / Total Size */}
        <div className="col-span-3 lg:col-span-1 text-right text-zinc-300">
          {formatBytes(torrent.totalSize)}
        </div>

        {/* DOWN Speed + Per-Row SVG Sparkline */}
        <div className="col-span-3 lg:col-span-1 flex items-center justify-end gap-1.5 text-right text-emerald-400 font-medium">
          <RowVelocitySparkline
            history={rateHistoryRef.current}
            active={!isPaused && torrent.downBytesPs > 0}
          />
          <span className="min-w-17">{formatRate(torrent.downBytesPs)}</span>
        </div>

        {/* UP Speed */}
        <div className="col-span-3 lg:col-span-1 text-right text-zinc-300">
          {formatRate(torrent.upBytesPs)}
        </div>

        {/* ETA */}
        <div className="col-span-3 lg:col-span-1 text-right text-zinc-300">
          {formatETA(torrent.etaSeconds)}
        </div>

        {/* SWARM Peers */}
        <div className="col-span-4 lg:col-span-1 text-right text-zinc-300">
          {torrent.activePeers}/{torrent.totalSwarmPeers}
        </div>

        {/* Controls */}
        <div className="col-span-8 lg:col-span-2 flex items-center justify-end gap-1">
          <Button
            variant={isPaused ? 'primary' : 'secondary'}
            size="xs"
            onClick={() => onTogglePause(torrent.id)}
          >
            {isPaused ? 'RESUME' : 'PAUSE'}
          </Button>
          <Button
            variant="secondary"
            size="xs"
            disabled={isStopped}
            onClick={() => onStop(torrent.id)}
            title="Halt swarm connections and flush disk buffers"
          >
            STOP
          </Button>
          <Button
            variant="danger"
            size="xs"
            onClick={() => {
              setDeleteFilesFromDisk(false);
              setConfirmDeleteOpen((prev) => !prev);
            }}
            title="Delete torrent from session"
          >
            DELETE
          </Button>
          <Button variant="ghost" size="xs" onClick={onToggleExpand}>
            {isExpanded ? 'HIDE' : 'INSPECT'}
          </Button>
        </div>
      </div>

      {/* Inline Delete Confirmation Popover */}
      {confirmDeleteOpen && (
        <div className="border-t border-red-900/60 bg-zinc-950 px-3 py-2.5 flex flex-wrap items-center justify-between gap-3 text-xs">
          <div className="flex flex-wrap items-center gap-4">
            <span className="text-red-400 font-semibold uppercase">
              DELETE TORRENT // {torrent.name}
            </span>
            <label className="flex items-center gap-2 text-zinc-300 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={deleteFilesFromDisk}
                onChange={(e) => setDeleteFilesFromDisk(e.target.checked)}
                className="accent-red-500"
              />
              Also delete downloaded files from disk ({torrent.outputDir})
            </label>
          </div>
          <div className="flex items-center gap-1.5">
            <Button
              type="button"
              variant="secondary"
              size="xs"
              onClick={() => setConfirmDeleteOpen(false)}
            >
              CANCEL
            </Button>
            <Button
              type="button"
              variant="danger"
              size="xs"
              onClick={() => {
                setConfirmDeleteOpen(false);
                onRemove(torrent.id, deleteFilesFromDisk);
              }}
            >
              {deleteFilesFromDisk ? 'DELETE + PURGE FILES' : 'CONFIRM DELETE'}
            </Button>
          </div>
        </div>
      )}

      {/* Expandable Hardware Telemetry Drawer */}
      {isExpanded && (
        <div className="border-t border-zinc-800 bg-zinc-950 p-3 flex flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-zinc-800 pb-2">
            <div className="flex items-center gap-1">
              {(['PIECES', 'PEERS', 'FILES', 'DIR'] as const).map((tab) => (
                <Button
                  key={tab}
                  variant={inspectorTab === tab ? 'primary' : 'secondary'}
                  size="xs"
                  onClick={() => setInspectorTab(tab)}
                >
                  {tab === 'PEERS'
                    ? `SWARM PEERS (${torrent.peers?.length ?? 0})`
                    : tab === 'FILES'
                    ? `FILE EXTENTS (${torrent.files?.length ?? 0})`
                    : tab}
                </Button>
              ))}
            </div>

            <div className="flex items-center gap-3 text-[11px] text-zinc-400">
              <Button
                variant={torrent.streamMode ? 'primary' : 'secondary'}
                size="xs"
                onClick={() => void onToggleStreamMode(torrent.id, !torrent.streamMode)}
                title="Prioritize pieces in linear order (0 -> N) with 16-piece sliding lookahead for mpv/vlc"
              >
                {torrent.streamMode ? 'STREAM LOOKAHEAD: ON' : 'STREAM MODE'}
              </Button>
              <span>VERIFIED: {formatBytes(torrent.verifiedBytes)}</span>
              <span>DISK Q: {torrent.diskWriteQueue}</span>
              <span>HASH Q: {torrent.hashQueue}</span>
              <span>IOPS: {torrent.iops}</span>
            </div>
          </div>

          {inspectorTab === 'PIECES' && (
            <PieceMap
              pieceStates={torrent.pieceStates || []}
              pieceAvailability={torrent.pieceAvailability}
              pieceCount={torrent.pieceCount}
              pieceLength={torrent.pieceLength}
              completedPieces={torrent.completedPieces}
              inFlightPieces={torrent.inFlightPieces}
              endgameActive={torrent.endgameActive}
              sequentialMode={torrent.sequentialMode}
            />
          )}

          {inspectorTab === 'PEERS' && (
            <div className="overflow-x-auto border border-zinc-800">
              <table className="w-full text-left text-xs border-collapse">
                <thead>
                  <tr className="bg-zinc-900 border-b border-zinc-800 text-[10px] text-zinc-500 uppercase">
                    <th className="py-1.5 px-2.5">PEER ADDR</th>
                    <th className="py-1.5 px-2.5">CLIENT</th>
                    <th className="py-1.5 px-2.5">PROTO</th>
                    <th className="py-1.5 px-2.5 text-right">DOWN</th>
                    <th className="py-1.5 px-2.5 text-right">UP</th>
                    <th className="py-1.5 px-2.5 text-right">RTT</th>
                    <th className="py-1.5 px-2.5 text-right">BDP PIPELINE</th>
                    <th className="py-1.5 px-2.5 text-right">HAVE</th>
                  </tr>
                </thead>
                <tbody>
                  {!torrent.peers || torrent.peers.length === 0 ? (
                    <tr>
                      <td colSpan={8} className="py-4 px-2.5 text-center text-zinc-500">
                        NO ACTIVE PEER CONNECTIONS IN SWARM WINDOW
                      </td>
                    </tr>
                  ) : (
                    torrent.peers.map((p) => (
                      <tr key={p.id} className="border-b border-zinc-800/60 last:border-b-0">
                        <td className="py-1.5 px-2.5 text-zinc-200">{p.addr}</td>
                        <td className="py-1.5 px-2.5 text-zinc-400">{p.client}</td>
                        <td className="py-1.5 px-2.5 text-emerald-400">{p.transport}</td>
                        <td className="py-1.5 px-2.5 text-right text-emerald-400">
                          {formatRate(p.downBytesPs)}
                        </td>
                        <td className="py-1.5 px-2.5 text-right text-zinc-300">
                          {formatRate(p.upBytesPs)}
                        </td>
                        <td className="py-1.5 px-2.5 text-right text-zinc-300">
                          {p.rttMs.toFixed(1)} ms
                        </td>
                        <td className="py-1.5 px-2.5 text-right text-zinc-300">
                          {p.inFlightBlocks}/{p.bdpQueueDepth}
                        </td>
                        <td className="py-1.5 px-2.5 text-right text-zinc-200">
                          {formatPercent(p.progress)}
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          )}

          {inspectorTab === 'FILES' && (
            <div className="overflow-x-auto border border-zinc-800">
              <table className="w-full text-left text-xs border-collapse">
                <thead>
                  <tr className="bg-zinc-900 border-b border-zinc-800 text-[10px] text-zinc-500 uppercase">
                    <th className="py-1.5 px-2.5">RELATIVE PATH</th>
                    <th className="py-1.5 px-2.5">PRIORITY</th>
                    <th className="py-1.5 px-2.5 text-right">EXTENT SIZE</th>
                    <th className="py-1.5 px-2.5 text-right">ON DISK</th>
                    <th className="py-1.5 px-2.5">ALLOCATION</th>
                    <th className="py-1.5 px-2.5 text-right">PROGRESS</th>
                  </tr>
                </thead>
                <tbody>
                  {!torrent.files || torrent.files.length === 0 ? (
                    <tr>
                      <td colSpan={6} className="py-4 px-2.5 text-center text-zinc-500">
                        METADATA PENDING // NO FILE EXTENTS ALLOCATED
                      </td>
                    </tr>
                  ) : (
                    torrent.files.map((f, fIdx) => {
                      const fileIndex = f.index ?? fIdx;
                      const currentPrio = f.priority ?? 'normal';
                      return (
                        <tr key={f.path} className="border-b border-zinc-800/60 last:border-b-0">
                          <td className="py-1.5 px-2.5 text-zinc-200">{f.path}</td>
                          <td className="py-1.5 px-2.5">
                            <div className="inline-flex items-center gap-1">
                              {(['skip', 'normal', 'high'] as const).map((prio) => (
                                <button
                                  key={prio}
                                  type="button"
                                  onClick={() =>
                                    void onSetFilePriority(torrent.id, fileIndex, prio)
                                  }
                                  className={`px-1.5 py-0.5 border text-[9px] uppercase cursor-pointer ${
                                    currentPrio === prio
                                      ? prio === 'skip'
                                        ? 'border-zinc-500 bg-zinc-800 text-zinc-200'
                                        : 'border-emerald-500 bg-emerald-500/15 text-emerald-300'
                                      : 'border-zinc-800 bg-zinc-950 text-zinc-500 hover:text-zinc-300'
                                  }`}
                                >
                                  {prio}
                                </button>
                              ))}
                            </div>
                          </td>
                          <td className="py-1.5 px-2.5 text-right text-zinc-300">
                            {formatBytes(f.length)}
                          </td>
                          <td className="py-1.5 px-2.5 text-right text-zinc-300">
                            {formatBytes(f.bytesOnDisk)}
                          </td>
                          <td className="py-1.5 px-2.5 text-emerald-400">
                            {f.preallocated ? 'SPARSE+EXTENT' : 'PENDING'}
                          </td>
                          <td className="py-1.5 px-2.5 text-right text-zinc-200">
                            {formatPercent(f.progress)}
                          </td>
                        </tr>
                      );
                    })
                  )}
                </tbody>
              </table>
            </div>
          )}

          {inspectorTab === 'DIR' && (
            <form
              onSubmit={handleRelocateSubmit}
              className="bg-zinc-900 border border-zinc-800 p-3 flex flex-col gap-2.5"
            >
              <Input
                label="RELOCATE TORRENT OUTPUT DIRECTORY (MOVES PRE-ALLOCATED EXTENTS)"
                value={newDirInput}
                onChange={(e) => setNewDirInput(e.target.value)}
              />
              {savedDirs.length > 0 && (
                <div className="flex flex-wrap items-center gap-1.5 text-[10px]">
                  <span className="text-zinc-500">QUICK SELECT:</span>
                  {savedDirs.map((d) => (
                    <button
                      key={d}
                      type="button"
                      onClick={() => setNewDirInput(d)}
                      className="px-2 py-0.5 border border-zinc-800 bg-zinc-950 text-zinc-400 hover:text-zinc-200 cursor-pointer"
                    >
                      {d}
                    </button>
                  ))}
                </div>
              )}
              <div className="flex justify-end">
                <Button type="submit" variant="primary" size="sm" disabled={isRelocating}>
                  {isRelocating ? 'RELOCATING EXTENTS...' : 'APPLY OUTPUT DIR'}
                </Button>
              </div>
            </form>
          )}
        </div>
      )}
    </div>
  );
};
