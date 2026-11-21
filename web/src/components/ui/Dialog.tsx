import React, { useEffect } from 'react';
import { Button } from './Button';

export interface DialogProps {
  open: boolean;
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}

export const Dialog: React.FC<DialogProps> = ({ open, title, onClose, children }) => {
  useEffect(() => {
    if (!open) return;
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose();
      }
    };
    window.addEventListener('keydown', handleKey);
    return () => window.removeEventListener('keydown', handleKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-950/85 p-4">
      <div className="w-full max-w-xl bg-zinc-900 border border-zinc-800">
        <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-2.5 bg-zinc-950">
          <span className="font-mono text-xs font-semibold uppercase tracking-wider text-zinc-100">
            {title}
          </span>
          <Button variant="ghost" size="xs" onClick={onClose}>
            [ESC] CLOSE
          </Button>
        </div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
};
