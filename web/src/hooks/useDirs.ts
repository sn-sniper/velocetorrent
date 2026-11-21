import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchDirs, saveOutputDir, validateDirectoryPath } from '../lib/api';
import type { DirectoryValidationResult } from '../types/telemetry';
import { ENGINE_STATE_QUERY_KEY } from './useTelemetryStream';

export const DIRS_QUERY_KEY = ['engine-dirs'] as const;

export function useDirs() {
  const queryClient = useQueryClient();
  const [dirError, setDirError] = useState<string | null>(null);

  const dirsQuery = useQuery({
    queryKey: DIRS_QUERY_KEY,
    queryFn: fetchDirs,
    staleTime: 3000,
  });

  const saveDirMutation = useMutation({
    mutationFn: ({ path, setDefault }: { path: string; setDefault: boolean }) =>
      saveOutputDir(path, setDefault),
    onSuccess: (data) => {
      setDirError(null);
      queryClient.setQueryData(DIRS_QUERY_KEY, data);
      void queryClient.invalidateQueries({ queryKey: ENGINE_STATE_QUERY_KEY });
    },
    onError: (err: Error) => {
      setDirError(err.message);
    },
  });

  return {
    dirsData: dirsQuery.data ?? null,
    isLoadingDirs: dirsQuery.isLoading,
    dirError,
    clearDirError: () => setDirError(null),
    saveDir: saveDirMutation.mutateAsync,
    isSavingDir: saveDirMutation.isPending,
  };
}

export function useDirectoryValidator(inputPath: string) {
  const [validation, setValidation] = useState<DirectoryValidationResult | null>(null);
  const [isValidating, setIsValidating] = useState(false);

  useEffect(() => {
    const trimmed = inputPath.trim();
    if (!trimmed) {
      setValidation(null);
      return;
    }

    let cancelled = false;
    setIsValidating(true);

    const timer = window.setTimeout(async () => {
      try {
        const res = await validateDirectoryPath(trimmed, false);
        if (!cancelled) {
          setValidation(res);
        }
      } catch (err) {
        if (!cancelled) {
          setValidation({
            path: trimmed,
            valid: false,
            writable: false,
            exists: false,
            freeBytes: 0,
            totalBytes: 0,
            errorMsg: err instanceof Error ? err.message : 'VALIDATION FAILED',
          });
        }
      } finally {
        if (!cancelled) {
          setIsValidating(false);
        }
      }
    }, 180);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [inputPath]);

  return { validation, isValidating };
}
