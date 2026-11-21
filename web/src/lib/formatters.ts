export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return '0 B';
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let idx = 0;
  let val = bytes;
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024;
    idx++;
  }
  if (idx === 0) {
    return `${Math.round(val)} B`;
  }
  return `${val.toFixed(1)} ${units[idx]}`;
}

export function formatRate(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) {
    return '0.0 KB/s';
  }
  const kb = bytesPerSec / 1024;
  if (kb < 1000) {
    return `${kb.toFixed(1)} KB/s`;
  }
  const mb = kb / 1024;
  if (mb < 1000) {
    return `${mb.toFixed(1)} MB/s`;
  }
  const gb = mb / 1024;
  return `${gb.toFixed(2)} GB/s`;
}

export function formatETA(seconds: number): string {
  if (seconds === 0) {
    return '00:00';
  }
  if (!Number.isFinite(seconds) || seconds < 0 || seconds > 86400 * 30) {
    return 'INF';
  }
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  const mm = String(m).padStart(2, '0');
  const ss = String(s).padStart(2, '0');
  if (h > 0) {
    const hh = String(h).padStart(2, '0');
    return `${hh}:${mm}:${ss}`;
  }
  return `${mm}:${ss}`;
}

export function formatPercent(pct: number): string {
  if (!Number.isFinite(pct) || pct <= 0) {
    return '0.0%';
  }
  if (pct >= 100) {
    return '100.0%';
  }
  return `${pct.toFixed(1)}%`;
}

export function validateMagnetInput(input: string): { valid: boolean; reason?: string } {
  const trimmed = input.trim();
  if (!trimmed) {
    return { valid: false, reason: 'EMPTY INPUT' };
  }
  if (trimmed.toLowerCase().startsWith('magnet:?')) {
    if (!trimmed.toLowerCase().includes('xt=urn:btih:')) {
      return { valid: false, reason: 'MISSING xt=urn:btih: TOPIC IN MAGNET URI' };
    }
    return { valid: true };
  }
  if (trimmed.toLowerCase().endsWith('.torrent')) {
    return { valid: true };
  }
  return {
    valid: false,
    reason: 'EXPECTED MAGNET:?xt=urn:btih:... URI OR .TORRENT FILE PATH',
  };
}
