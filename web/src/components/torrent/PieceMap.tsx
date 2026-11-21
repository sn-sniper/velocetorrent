import React, { useMemo } from 'react';
import { formatBytes } from '../../lib/formatters';

interface PieceMapProps {
  pieceStates: number[];
  pieceAvailability?: number[];
  pieceCount: number;
  pieceLength: number;
  completedPieces: number;
  inFlightPieces: number;
  endgameActive: boolean;
  sequentialMode: boolean;
}

export const PieceMap: React.FC<PieceMapProps> = ({
  pieceStates,
  pieceAvailability,
  pieceCount,
  pieceLength,
  completedPieces,
  inFlightPieces,
  endgameActive,
  sequentialMode,
}) => {
  // Bucket arbitrary piece counts into up to 256 visual cells so rendering stays 60fps
  const cells = useMemo(() => {
    const maxCells = Math.min(Math.max(pieceStates.length, 1), 256);
    if (pieceStates.length === 0) {
      return [];
    }
    const result: Array<{ status: number; avail: number; start: number; end: number }> = [];
    for (let i = 0; i < maxCells; i++) {
      const start = Math.floor((i * pieceStates.length) / maxCells);
      const end = Math.max(
        start + 1,
        Math.floor(((i + 1) * pieceStates.length) / maxCells)
      );
      let minState = 3;
      let maxState = 0;
      let maxAvail = 0;
      for (let p = start; p < end && p < pieceStates.length; p++) {
        const s = pieceStates[p];
        if (s < minState) minState = s;
        if (s > maxState) maxState = s;
        const a = pieceAvailability?.[p] ?? 0;
        if (a > maxAvail) maxAvail = a;
      }
      const status = minState === 3 ? 3 : maxState;
      result.push({ status, avail: maxAvail, start, end: end - 1 });
    }
    return result;
  }, [pieceStates, pieceAvailability]);

  return (
    <div className="bg-zinc-950 border border-zinc-800 p-3 flex flex-col gap-2.5 font-mono tabular-nums">
      <div className="flex flex-wrap items-center justify-between gap-2 text-[11px]">
        <div className="flex items-center gap-4">
          <span className="text-zinc-500 font-semibold">PIECES BITFIELD</span>
          <span className="text-zinc-300">
            VERIFIED: <strong className="text-emerald-400">{completedPieces}</strong>/{pieceCount}
          </span>
          <span className="text-zinc-300">
            IN-FLIGHT: <strong className="text-amber-400">{inFlightPieces}</strong>
          </span>
          <span className="text-zinc-400">
            BLOCK UNIT: {formatBytes(pieceLength)}
          </span>
        </div>

        <div className="flex items-center gap-3 text-[10px]">
          {endgameActive && (
            <span className="px-1.5 py-0.5 border border-amber-500/60 bg-amber-500/10 text-amber-400 font-semibold">
              ENDGAME PIPELINE
            </span>
          )}
          {sequentialMode && (
            <span className="px-1.5 py-0.5 border border-emerald-500/60 bg-emerald-500/10 text-emerald-400 font-semibold">
              SEQUENTIAL
            </span>
          )}
          <span className="flex items-center gap-1 text-zinc-400">
            <span className="w-2.5 h-2.5 bg-emerald-500 inline-block" /> VERIFIED
          </span>
          <span className="flex items-center gap-1 text-zinc-400">
            <span className="w-2.5 h-2.5 bg-amber-500 inline-block" /> IN-FLIGHT / HASH
          </span>
          <span className="flex items-center gap-1 text-zinc-400">
            <span className="w-2.5 h-2.5 bg-zinc-700 inline-block" /> SWARM AVAIL
          </span>
          <span className="flex items-center gap-1 text-zinc-400">
            <span className="w-2.5 h-2.5 bg-zinc-900 border border-zinc-800 inline-block" /> MISSING
          </span>
        </div>
      </div>

      {cells.length === 0 ? (
        <div className="text-xs text-zinc-500 py-2">
          AWAITING METADATA PIECE GEOMETRY...
        </div>
      ) : (
        <div
          className="grid gap-[2px]"
          style={{
            gridTemplateColumns: 'repeat(auto-fill, minmax(10px, 1fr))',
          }}
        >
          {cells.map((cell, idx) => {
            let bgClass = 'bg-zinc-900 border border-zinc-800/80';
            if (cell.status === 3) {
              bgClass = 'bg-emerald-500';
            } else if (cell.status === 2 || cell.status === 1) {
              bgClass = 'bg-amber-500';
            } else if (cell.avail > 0) {
              bgClass = 'bg-zinc-700';
            }
            return (
              <div
                key={idx}
                className={`h-3 w-full ${bgClass}`}
                title={`Piece ${cell.start}${cell.end > cell.start ? `..${cell.end}` : ''} | Avail: ${cell.avail}`}
              />
            );
          })}
        </div>
      )}
    </div>
  );
};
