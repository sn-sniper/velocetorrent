import React from 'react';
import { Button } from '../ui/Button';

interface AppShellProps {
  topBar: React.ReactNode;
  errorBanner?: string | null;
  onDismissError?: () => void;
  children: React.ReactNode;
}

export const AppShell: React.FC<AppShellProps> = ({
  topBar,
  errorBanner,
  onDismissError,
  children,
}) => {
  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-100 flex flex-col">
      {topBar}

      {errorBanner && (
        <div className="bg-red-950/40 border-b border-red-500/60 px-4 py-2">
          <div className="max-w-[1600px] mx-auto flex items-center justify-between gap-4 font-mono text-xs text-red-300">
            <div className="flex items-center gap-2">
              <span className="px-1.5 py-0.5 bg-red-500/20 border border-red-500/50 text-red-400 font-bold text-[10px]">
                ERROR
              </span>
              <span>{errorBanner}</span>
            </div>
            {onDismissError && (
              <Button variant="ghost" size="xs" onClick={onDismissError}>
                DISMISS
              </Button>
            )}
          </div>
        </div>
      )}

      <main className="flex-1 max-w-[1600px] w-full mx-auto px-4 py-4 flex flex-col gap-4">
        {children}
      </main>
    </div>
  );
};
