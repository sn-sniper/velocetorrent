import React, { useEffect, useRef, useState } from 'react';
import { useDirectoryValidator } from '../../hooks/useDirs';
import { formatBytes, validateMagnetInput } from '../../lib/formatters';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';

interface DropzoneProps {
  defaultOutputDir: string;
  savedDirs: string[];
  isEmptyState: boolean;
  isSubmitting: boolean;
  onAddMagnetOrSource: (params: {
    source: string;
    outputDir: string;
    sequential: boolean;
  }) => Promise<unknown>;
  onUploadTorrentFile: (params: {
    file: File;
    outputDir: string;
    sequential: boolean;
  }) => Promise<unknown>;
  onSpawnTestSwarm: () => void;
}

export const Dropzone: React.FC<DropzoneProps> = ({
  defaultOutputDir,
  savedDirs,
  isEmptyState,
  isSubmitting,
  onAddMagnetOrSource,
  onUploadTorrentFile,
  onSpawnTestSwarm,
}) => {
  const [sourceInput, setSourceInput] = useState('');
  const [outputDir, setOutputDir] = useState(defaultOutputDir || '');
  const [sequential, setSequential] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!outputDir && defaultOutputDir) {
      setOutputDir(defaultOutputDir);
    }
  }, [defaultOutputDir, outputDir]);

  const { validation } = useDirectoryValidator(outputDir);

  const handleMagnetSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLocalError(null);
    const check = validateMagnetInput(sourceInput);
    if (!check.valid) {
      setLocalError(check.reason || 'INVALID INPUT');
      return;
    }
    try {
      await onAddMagnetOrSource({
        source: sourceInput.trim(),
        outputDir: outputDir.trim(),
        sequential,
      });
      setSourceInput('');
    } catch {
      // Error surfaced via parent actionError banner
    }
  };

  const handleFileSelect = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    setLocalError(null);
    const file = files[0];
    if (!file.name.toLowerCase().endsWith('.torrent')) {
      setLocalError(`INVALID FILE "${file.name}": EXPECTED BENCODED .TORRENT FILE`);
      return;
    }
    try {
      await onUploadTorrentFile({
        file,
        outputDir: outputDir.trim(),
        sequential,
      });
    } catch {
      // Error surfaced via parent actionError banner
    }
  };

  const onDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setIsDragging(false);
    void handleFileSelect(e.dataTransfer.files);
  };

  return (
    <section className="bg-zinc-900 border border-zinc-800 p-4 flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-zinc-800 pb-2">
        <div className="flex items-center gap-2 font-mono text-xs">
          <span className="text-emerald-400 font-bold">+</span>
          <span className="font-semibold uppercase tracking-wider text-zinc-200">
            QUEUE TRANSFER // .TORRENT OR MAGNET:?
          </span>
        </div>

        <label className="flex items-center gap-2 font-mono text-[11px] text-zinc-400 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={sequential}
            onChange={(e) => setSequential(e.target.checked)}
            className="accent-emerald-500"
          />
          SEQUENTIAL PIECE ORDER
        </label>
      </div>

      {/* Magnet / File Path Input & Target Output Dir */}
      <form onSubmit={handleMagnetSubmit} className="grid grid-cols-1 lg:grid-cols-12 gap-3">
        <div className="lg:col-span-6">
          <Input
            label="SOURCE (MAGNET:? URI OR LOCAL .TORRENT PATH)"
            placeholder="magnet:?xt=urn:btih:... or /path/to/archive.torrent"
            value={sourceInput}
            onChange={(e) => {
              setSourceInput(e.target.value);
              if (localError) setLocalError(null);
            }}
          />
        </div>

        <div className="lg:col-span-4 flex flex-col gap-1">
          <Input
            label="OUTPUT DIR (SPARSE PRE-ALLOCATION TARGET)"
            placeholder={defaultOutputDir || '/path/to/downloads'}
            value={outputDir}
            onChange={(e) => setOutputDir(e.target.value)}
          />
          {validation && (
            <div className="flex items-center justify-between font-mono text-[10px] tabular-nums">
              <span className={validation.valid ? 'text-emerald-400' : 'text-red-400'}>
                {validation.valid
                  ? validation.exists
                    ? 'WRITABLE DIR'
                    : 'WILL CREATE DIR'
                  : validation.errorMsg || 'INVALID PATH'}
              </span>
              {validation.freeBytes > 0 && (
                <span className="text-zinc-400">
                  FREE: {formatBytes(validation.freeBytes)}
                </span>
              )}
            </div>
          )}
        </div>

        <div className="lg:col-span-2 flex items-end gap-2">
          <Button
            type="submit"
            variant="primary"
            size="md"
            className="w-full h-[30px]"
            disabled={isSubmitting}
          >
            {isSubmitting ? 'QUEUEING...' : 'ADD TRANSFER'}
          </Button>
        </div>
      </form>

      {/* Quick-select saved directories & autocomplete suggestions */}
      {(savedDirs.length > 0 || (validation?.suggestions && validation.suggestions.length > 0)) && (
        <div className="flex flex-wrap items-center gap-1.5 font-mono text-[11px]">
          <span className="text-zinc-500 mr-1">SAVED DIRS:</span>
          {savedDirs.map((dir) => (
            <button
              key={dir}
              type="button"
              onClick={() => setOutputDir(dir)}
              className={`px-2 py-0.5 border text-[10px] cursor-pointer transition-colors ${
                outputDir === dir
                  ? 'border-emerald-500/70 bg-emerald-500/10 text-emerald-300'
                  : 'border-zinc-800 bg-zinc-950 text-zinc-400 hover:border-zinc-700 hover:text-zinc-200'
              }`}
            >
              {dir}
            </button>
          ))}
          {validation?.suggestions?.slice(0, 4).map((sug) => {
            if (savedDirs.includes(sug)) return null;
            return (
              <button
                key={sug}
                type="button"
                onClick={() => setOutputDir(sug)}
                className="px-2 py-0.5 border border-zinc-800/70 bg-zinc-950 text-zinc-500 hover:text-zinc-300 text-[10px] cursor-pointer"
              >
                ↳ {sug}
              </button>
            );
          })}
        </div>
      )}

      {localError && (
        <div className="bg-red-950/30 border border-red-500/60 px-3 py-1.5 font-mono text-xs text-red-400">
          {localError}
        </div>
      )}

      {/* Functional .torrent File Dropzone */}
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setIsDragging(true);
        }}
        onDragLeave={() => setIsDragging(false)}
        onDrop={onDrop}
        onClick={() => fileInputRef.current?.click()}
        className={`border border-dashed transition-colors cursor-pointer flex flex-col items-center justify-center font-mono ${
          isEmptyState ? 'py-10' : 'py-3'
        } ${
          isDragging
            ? 'border-emerald-500 bg-emerald-500/10 text-emerald-300'
            : 'border-zinc-800 bg-zinc-950 hover:border-zinc-600 text-zinc-400'
        }`}
      >
        <input
          ref={fileInputRef}
          type="file"
          accept=".torrent,application/x-bittorrent"
          className="hidden"
          onChange={(e) => void handleFileSelect(e.target.files)}
        />
        <div className="flex flex-wrap items-center justify-center gap-3 text-xs">
          <span className="text-zinc-200 font-semibold">
            DROP .TORRENT FILE HERE OR CLICK TO BROWSE
          </span>
          <span className="text-zinc-600">|</span>
          <span className="text-zinc-500">
            SHA-1 / SHA-256 PIECE VERIFICATION + SPARSE PRE-ALLOCATION
          </span>
        </div>

        {isEmptyState && (
          <div className="mt-4 flex items-center gap-3" onClick={(e) => e.stopPropagation()}>
            <span className="text-[11px] text-zinc-500">
              NO ACTIVE TRANSFERS IN SESSION
            </span>
            <Button variant="secondary" size="xs" onClick={onSpawnTestSwarm}>
              SPAWN LOCAL 24 MiB SWARM TRANSFER
            </Button>
          </div>
        )}
      </div>
    </section>
  );
};
