import type {
  APIErrorPayload,
  DirectoryValidationResult,
  DirsResponse,
  EngineTelemetrySnapshot,
  FilePriority,
  ThrottleSnapshot,
  TorrentTelemetrySnapshot,
} from '../types/telemetry';

export class IPCRequestError extends Error {
  public readonly code: string;
  public readonly status: number;

  constructor(message: string, code = 'IPC_ERROR', status = 400) {
    super(message);
    this.name = 'IPCRequestError';
    this.code = code;
    this.status = status;
  }
}

const API_BASE = import.meta.env.VITE_API_BASE || '';

async function handleResponse<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let payload: APIErrorPayload | null = null;
    try {
      payload = (await res.json()) as APIErrorPayload;
    } catch {
      // Fallback if response is plain text
    }
    const msg = payload?.error || `IPC HTTP ${res.status}: ${res.statusText}`;
    const code = payload?.code || 'IPC_HTTP_ERROR';
    throw new IPCRequestError(msg, code, res.status);
  }
  return (await res.json()) as T;
}

export async function fetchEngineState(): Promise<EngineTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/state`);
  return handleResponse<EngineTelemetrySnapshot>(res);
}

export async function addTorrentBySource(params: {
  source: string;
  outputDir?: string;
  sequential?: boolean;
}): Promise<TorrentTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/torrents`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      source: params.source,
      outputDir: params.outputDir || '',
      sequential: Boolean(params.sequential),
    }),
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function uploadTorrentFile(params: {
  file: File;
  outputDir?: string;
  sequential?: boolean;
}): Promise<TorrentTelemetrySnapshot> {
  const form = new FormData();
  form.append('file', params.file);
  if (params.outputDir) {
    form.append('outputDir', params.outputDir);
  }
  if (params.sequential) {
    form.append('sequential', 'true');
  }
  const res = await fetch(`${API_BASE}/api/torrents`, {
    method: 'POST',
    body: form,
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function spawnSyntheticSwarm(params: {
  name?: string;
  sizeBytes?: number;
  outputDir?: string;
  sequential?: boolean;
}): Promise<TorrentTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/torrents/synthetic`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: params.name || `veloce-swarm-${Date.now().toString().slice(-4)}.iso`,
      sizeBytes: params.sizeBytes || 24 * 1024 * 1024,
      outputDir: params.outputDir || '',
      sequential: Boolean(params.sequential),
    }),
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function toggleTorrentPause(id: string): Promise<TorrentTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/torrents/${encodeURIComponent(id)}/toggle`, {
    method: 'POST',
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function relocateTorrentDir(
  id: string,
  outputDir: string
): Promise<TorrentTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/torrents/${encodeURIComponent(id)}/dir`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ outputDir }),
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function setTorrentFilePriority(
  id: string,
  fileIndex: number,
  priority: FilePriority
): Promise<TorrentTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/torrents/${encodeURIComponent(id)}/files`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ fileIndex, priority }),
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function setTorrentStreamMode(
  id: string,
  enabled: boolean
): Promise<TorrentTelemetrySnapshot> {
  const res = await fetch(`${API_BASE}/api/torrents/${encodeURIComponent(id)}/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  return handleResponse<TorrentTelemetrySnapshot>(res);
}

export async function toggleBandwidthThrottle(): Promise<ThrottleSnapshot> {
  const res = await fetch(`${API_BASE}/api/throttle`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ toggle: true }),
  });
  return handleResponse<ThrottleSnapshot>(res);
}

export async function deleteTorrent(id: string, deleteData = false): Promise<void> {
  const res = await fetch(
    `${API_BASE}/api/torrents/${encodeURIComponent(id)}?deleteData=${deleteData}`,
    {
      method: 'DELETE',
    }
  );
  await handleResponse<{ deleted: string }>(res);
}

export async function fetchDirs(): Promise<DirsResponse> {
  const res = await fetch(`${API_BASE}/api/dirs`);
  return handleResponse<DirsResponse>(res);
}

export async function saveOutputDir(path: string, setDefault = true): Promise<DirsResponse> {
  const res = await fetch(`${API_BASE}/api/dirs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, setDefault }),
  });
  return handleResponse<DirsResponse>(res);
}

export async function validateDirectoryPath(
  path: string,
  create = false
): Promise<DirectoryValidationResult> {
  const q = new URLSearchParams({ path, create: String(create) });
  const res = await fetch(`${API_BASE}/api/dirs/validate?${q.toString()}`);
  return handleResponse<DirectoryValidationResult>(res);
}
