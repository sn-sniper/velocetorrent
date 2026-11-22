import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  addTorrentBySource,
  deleteTorrent,
  relocateTorrentDir,
  setTorrentFilePriority,
  setTorrentStreamMode,
  spawnSyntheticSwarm,
  stopTorrent,
  toggleBandwidthThrottle,
  toggleTorrentPause,
  uploadTorrentFile,
} from '../lib/api';
import type { FilePriority } from '../types/telemetry';
import { ENGINE_STATE_QUERY_KEY } from './useTelemetryStream';

export function useTorrents() {
  const queryClient = useQueryClient();
  const [actionError, setActionError] = useState<string | null>(null);

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ENGINE_STATE_QUERY_KEY });
  };

  const addSourceMutation = useMutation({
    mutationFn: addTorrentBySource,
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const uploadFileMutation = useMutation({
    mutationFn: uploadTorrentFile,
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const syntheticMutation = useMutation({
    mutationFn: spawnSyntheticSwarm,
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const togglePauseMutation = useMutation({
    mutationFn: (id: string) => toggleTorrentPause(id),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const stopMutation = useMutation({
    mutationFn: (id: string) => stopTorrent(id),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const relocateMutation = useMutation({
    mutationFn: ({ id, outputDir }: { id: string; outputDir: string }) =>
      relocateTorrentDir(id, outputDir),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: ({ id, deleteFiles }: { id: string; deleteFiles?: boolean }) =>
      deleteTorrent(id, Boolean(deleteFiles)),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const filePriorityMutation = useMutation({
    mutationFn: ({
      id,
      fileIndex,
      priority,
    }: {
      id: string;
      fileIndex: number;
      priority: FilePriority;
    }) => setTorrentFilePriority(id, fileIndex, priority),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const streamModeMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      setTorrentStreamMode(id, enabled),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  const throttleMutation = useMutation({
    mutationFn: () => toggleBandwidthThrottle(),
    onSuccess: () => {
      setActionError(null);
      invalidate();
    },
    onError: (err: Error) => {
      setActionError(err.message);
    },
  });

  return {
    actionError,
    clearActionError: () => setActionError(null),
    addBySource: addSourceMutation.mutateAsync,
    uploadFile: uploadFileMutation.mutateAsync,
    spawnSynthetic: syntheticMutation.mutateAsync,
    togglePause: togglePauseMutation.mutateAsync,
    stopTorrent: stopMutation.mutateAsync,
    relocateDir: relocateMutation.mutateAsync,
    removeTorrent: deleteMutation.mutateAsync,
    setFilePriority: filePriorityMutation.mutateAsync,
    setStreamMode: streamModeMutation.mutateAsync,
    toggleThrottle: throttleMutation.mutateAsync,
    isSubmitting:
      addSourceMutation.isPending ||
      uploadFileMutation.isPending ||
      syntheticMutation.isPending ||
      relocateMutation.isPending,
  };
}
