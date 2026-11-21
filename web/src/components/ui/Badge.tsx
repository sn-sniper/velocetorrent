import React from 'react';
import type { TorrentStatus } from '../../types/telemetry';

export const StatusBadge: React.FC<{ status: TorrentStatus }> = ({ status }) => {
  const styles: Record<TorrentStatus, string> = {
    DOWNLOADING: 'border-emerald-500/60 bg-emerald-500/10 text-emerald-400',
    SEEDING: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
    COMPLETED: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
    METADATA: 'border-amber-500/60 bg-amber-500/10 text-amber-400',
    PREALLOCATING: 'border-amber-500/60 bg-amber-500/10 text-amber-400',
    VERIFYING: 'border-amber-500/60 bg-amber-500/10 text-amber-400',
    PAUSED: 'border-zinc-700 bg-zinc-900 text-zinc-400',
    ERROR: 'border-red-500/70 bg-red-500/10 text-red-400',
  };

  return (
    <span
      className={`inline-flex items-center px-1.5 py-0.5 border font-mono text-[10px] font-semibold tracking-wider uppercase ${styles[status] || styles.PAUSED}`}
    >
      {status}
    </span>
  );
};
