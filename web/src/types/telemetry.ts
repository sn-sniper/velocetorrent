export type PieceStatus = 0 | 1 | 2 | 3;

export type TorrentStatus =
  | 'METADATA'
  | 'PREALLOCATING'
  | 'DOWNLOADING'
  | 'VERIFYING'
  | 'SEEDING'
  | 'PAUSED'
  | 'STOPPED'
  | 'COMPLETED'
  | 'ERROR';

export interface VelocitySample {
  timestampMs: number;
  downBytesPs: number;
  upBytesPs: number;
  iops: number;
}

export interface PeerTelemetrySnapshot {
  id: string;
  addr: string;
  client: string;
  transport: string;
  downBytesPs: number;
  upBytesPs: number;
  downloaded: number;
  uploaded: number;
  rttMs: number;
  bdpQueueDepth: number;
  inFlightBlocks: number;
  amChoking: boolean;
  amInterested: boolean;
  peerChoking: boolean;
  peerInterested: boolean;
  isOptimistic: boolean;
  progress: number;
}

export type FilePriority = 'skip' | 'normal' | 'high';

export interface FileTelemetrySnapshot {
  index?: number;
  path: string;
  length: number;
  offset: number;
  bytesOnDisk: number;
  preallocated: boolean;
  priority?: FilePriority;
  progress: number;
}

export interface ThrottleSnapshot {
  profile: 'UNCAPPED' | 'BACKGROUND' | 'CUSTOM';
  maxDownBytesPs: number;
  maxUpBytesPs: number;
}

export interface TorrentTelemetrySnapshot {
  id: string;
  infoHash: string;
  name: string;
  outputDir: string;
  status: TorrentStatus;
  errorMsg?: string;
  totalSize: number;
  downloaded: number;
  uploaded: number;
  verifiedBytes: number;
  downBytesPs: number;
  upBytesPs: number;
  progress: number;
  etaSeconds: number;
  pieceCount: number;
  pieceLength: number;
  completedPieces: number;
  inFlightPieces: number;
  activePeers: number;
  totalSwarmPeers: number;
  seeders: number;
  leechers: number;
  diskWriteQueue: number;
  hashQueue: number;
  iops: number;
  verifyLatencyUs?: number;
  endgameActive: boolean;
  sequentialMode: boolean;
  streamMode?: boolean;
  addedAt: number;
  completedAt?: number;
  pieceStates: number[];
  pieceAvailability?: number[];
  peers?: PeerTelemetrySnapshot[];
  files?: FileTelemetrySnapshot[];
}

export interface EngineTelemetrySnapshot {
  timestampMs: number;
  uptimeSeconds: number;
  defaultOutputDir: string;
  savedDirs: string[];
  diskFreeBytes: number;
  diskTotalBytes: number;
  globalDownBytesPs: number;
  globalUpBytesPs: number;
  totalDownloaded: number;
  totalUploaded: number;
  globalActivePeers: number;
  globalSwarmPeers: number;
  globalDiskQueue: number;
  globalHashQueue: number;
  globalIOPS: number;
  avgVerifyLatencyUs?: number;
  workerThreads: number;
  throttle?: ThrottleSnapshot;
  blocklistRanges?: number;
  blockedPeerHits?: number;
  velocityHistory: VelocitySample[];
  torrents: TorrentTelemetrySnapshot[];
}

export interface SavedDirEntry {
  path: string;
  isDefault: boolean;
  freeBytes: number;
  totalBytes: number;
  writable: boolean;
}

export interface DirsResponse {
  defaultOutputDir: string;
  dirs: SavedDirEntry[];
}

export interface DirectoryValidationResult {
  path: string;
  valid: boolean;
  writable: boolean;
  exists: boolean;
  freeBytes: number;
  totalBytes: number;
  errorMsg?: string;
  suggestions?: string[];
}

export interface APIErrorPayload {
  error: string;
  code: string;
}
