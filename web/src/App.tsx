import React, { useState } from 'react';
import { AppShell } from './components/layout/AppShell';
import { TopTelemetryBar } from './components/layout/TopTelemetryBar';
import { Dropzone } from './components/torrent/Dropzone';
import { TorrentRow } from './components/torrent/TorrentRow';
import { Button } from './components/ui/Button';
import { Dialog } from './components/ui/Dialog';
import { Input } from './components/ui/Input';
import { useDirectoryValidator, useDirs } from './hooks/useDirs';
import { useTelemetryStream } from './hooks/useTelemetryStream';
import { useTorrents } from './hooks/useTorrents';
import { formatBytes } from './lib/formatters';

export const App: React.FC = () => {
  const { telemetry, connectionState, daemonError } = useTelemetryStream();
  const {
    actionError,
    clearActionError,
    addBySource,
    uploadFile,
    spawnSynthetic,
    togglePause,
    relocateDir,
    removeTorrent,
    setFilePriority,
    setStreamMode,
    toggleThrottle,
    isSubmitting,
  } = useTorrents();
  const { dirsData, dirError, clearDirError, saveDir, isSavingDir } = useDirs();

  const [expandedIDs, setExpandedIDs] = useState<Record<string, boolean>>({});
  const [dirModalOpen, setDirModalOpen] = useState(false);
  const [dirModalInput, setDirModalInput] = useState('');
  const [setAsDefault, setSetAsDefault] = useState(true);

  const { validation: modalValidation } = useDirectoryValidator(dirModalInput);

  const torrents = telemetry?.torrents ?? [];
  const savedDirs = telemetry?.savedDirs ?? dirsData?.dirs.map((d) => d.path) ?? [];
  const defaultOutputDir =
    telemetry?.defaultOutputDir ?? dirsData?.defaultOutputDir ?? '';

  const combinedError = actionError || dirError || daemonError;

  const toggleExpand = (id: string) => {
    setExpandedIDs((prev) => ({
      ...prev,
      [id]: !prev[id],
    }));
  };

  const handleOpenDirModal = () => {
    setDirModalInput(defaultOutputDir);
    setDirModalOpen(true);
  };

  const handleSaveDirectory = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!dirModalInput.trim()) return;
    await saveDir({ path: dirModalInput.trim(), setDefault: setAsDefault });
    setDirModalOpen(false);
  };

  return (
    <AppShell
      errorBanner={combinedError}
      onDismissError={() => {
        clearActionError();
        clearDirError();
      }}
      topBar={
        <TopTelemetryBar
          telemetry={telemetry}
          connectionState={connectionState}
          onOpenDirManager={handleOpenDirModal}
          onSpawnTestSwarm={() => void spawnSynthetic({})}
          onToggleThrottle={() => void toggleThrottle()}
          isSpawningSwarm={isSubmitting}
        />
      }
    >
      {/* Functional Dropzone & Magnet Input Bar */}
      <Dropzone
        defaultOutputDir={defaultOutputDir}
        savedDirs={savedDirs}
        isEmptyState={torrents.length === 0}
        isSubmitting={isSubmitting}
        onAddMagnetOrSource={addBySource}
        onUploadTorrentFile={uploadFile}
        onSpawnTestSwarm={() => void spawnSynthetic({})}
      />

      {/* Active Transfers Table */}
      {torrents.length > 0 && (
        <section className="border border-zinc-800 bg-zinc-900">
          {/* Table Header */}
          <div className="grid grid-cols-12 items-center gap-2 px-3 py-2 border-b border-zinc-800 bg-zinc-950 font-mono text-[10px] font-semibold uppercase tracking-wider text-zinc-500">
            <div className="col-span-12 lg:col-span-3">
              TRANSFER ({torrents.length})
            </div>
            <div className="hidden lg:block lg:col-span-1">STATE</div>
            <div className="hidden lg:block lg:col-span-2">PIECES PROGRESS</div>
            <div className="hidden lg:block lg:col-span-1 text-right">SIZE</div>
            <div className="hidden lg:block lg:col-span-1 text-right">DOWN</div>
            <div className="hidden lg:block lg:col-span-1 text-right">UP</div>
            <div className="hidden lg:block lg:col-span-1 text-right">ETA</div>
            <div className="hidden lg:block lg:col-span-1 text-right">SWARM</div>
            <div className="hidden lg:block lg:col-span-1 text-right">CONTROL</div>
          </div>

          {/* Rows */}
          <div className="divide-y divide-zinc-800">
            {torrents.map((t, index) => (
              <TorrentRow
                key={t.id}
                torrent={t}
                savedDirs={savedDirs}
                isExpanded={expandedIDs[t.id] ?? index === 0}
                onToggleExpand={() => toggleExpand(t.id)}
                onTogglePause={(id) => void togglePause(id)}
                onRelocateDir={(id, newDir) => relocateDir({ id, outputDir: newDir })}
                onSetFilePriority={(id, fileIndex, priority) =>
                  setFilePriority({ id, fileIndex, priority })
                }
                onToggleStreamMode={(id, enabled) => setStreamMode({ id, enabled })}
                onRemove={(id, deleteData) => void removeTorrent({ id, deleteData })}
              />
            ))}
          </div>
        </section>
      )}

      {/* Directory Management Dialog */}
      <Dialog
        open={dirModalOpen}
        title="OUTPUT DIRECTORY MANAGEMENT // SPARSE EXTENT ALLOCATOR"
        onClose={() => setDirModalOpen(false)}
      >
        <form onSubmit={handleSaveDirectory} className="flex flex-col gap-4 font-mono">
          <div className="flex flex-col gap-1.5">
            <Input
              label="FILESYSTEM DIRECTORY PATH"
              value={dirModalInput}
              onChange={(e) => setDirModalInput(e.target.value)}
              placeholder="/path/to/output"
              autoFocus
            />

            {modalValidation && (
              <div className="flex items-center justify-between text-[11px] tabular-nums">
                <span
                  className={
                    modalValidation.valid ? 'text-emerald-400' : 'text-red-400'
                  }
                >
                  {modalValidation.valid
                    ? modalValidation.exists
                      ? 'VALID WRITABLE DIRECTORY'
                      : 'DIRECTORY WILL BE CREATED ON SAVE'
                    : modalValidation.errorMsg || 'INVALID DIRECTORY'}
                </span>
                {modalValidation.freeBytes > 0 && (
                  <span className="text-zinc-400">
                    FREE: {formatBytes(modalValidation.freeBytes)} /{' '}
                    {formatBytes(modalValidation.totalBytes)}
                  </span>
                )}
              </div>
            )}

            {modalValidation?.suggestions && modalValidation.suggestions.length > 0 && (
              <div className="flex flex-wrap gap-1 pt-1">
                {modalValidation.suggestions.map((sug) => (
                  <button
                    key={sug}
                    type="button"
                    onClick={() => setDirModalInput(sug)}
                    className="px-2 py-0.5 bg-zinc-950 border border-zinc-800 text-[10px] text-zinc-400 hover:text-zinc-100 cursor-pointer"
                  >
                    ↳ {sug}
                  </button>
                ))}
              </div>
            )}
          </div>

          <label className="flex items-center gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={setAsDefault}
              onChange={(e) => setSetAsDefault(e.target.checked)}
              className="accent-emerald-500"
            />
            SET AS GLOBAL DEFAULT OUTPUT DIRECTORY
          </label>

          {dirsData?.dirs && dirsData.dirs.length > 0 && (
            <div className="flex flex-col gap-1.5 border-t border-zinc-800 pt-3">
              <span className="text-[10px] text-zinc-500 uppercase">
                SAVED QUICK-SELECT DIRECTORIES
              </span>
              <div className="flex flex-col gap-1 max-h-40 overflow-y-auto">
                {dirsData.dirs.map((entry) => (
                  <div
                    key={entry.path}
                    onClick={() => setDirModalInput(entry.path)}
                    className="flex items-center justify-between px-2.5 py-1.5 bg-zinc-950 border border-zinc-800 hover:border-zinc-600 cursor-pointer text-xs tabular-nums"
                  >
                    <div className="flex items-center gap-2 truncate">
                      <span className="text-zinc-200 truncate">{entry.path}</span>
                      {entry.isDefault && (
                        <span className="px-1 py-0.2 bg-emerald-500/15 border border-emerald-500/60 text-emerald-400 text-[9px]">
                          DEFAULT
                        </span>
                      )}
                    </div>
                    <span className="text-zinc-400 text-[11px] shrink-0">
                      FREE {formatBytes(entry.freeBytes)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div className="flex items-center justify-end gap-2 pt-2 border-t border-zinc-800">
            <Button
              type="button"
              variant="secondary"
              onClick={() => setDirModalOpen(false)}
            >
              CANCEL
            </Button>
            <Button type="submit" variant="primary" disabled={isSavingDir}>
              {isSavingDir ? 'VERIFYING...' : 'SAVE DIRECTORY'}
            </Button>
          </div>
        </form>
      </Dialog>
    </AppShell>
  );
};
