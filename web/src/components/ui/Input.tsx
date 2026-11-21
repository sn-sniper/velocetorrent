import React from 'react';

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label?: string;
}

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ label, className = '', ...props }, ref) => {
    return (
      <div className="flex flex-col gap-1 w-full">
        {label && (
          <label className="font-mono text-[11px] uppercase tracking-wider text-zinc-500">
            {label}
          </label>
        )}
        <input
          ref={ref}
          className={`w-full bg-zinc-950 border border-zinc-800 px-3 py-1.5 font-mono text-xs text-zinc-100 placeholder:text-zinc-600 focus:outline-none focus:border-emerald-500 transition-colors ${className}`}
          {...props}
        />
      </div>
    );
  }
);

Input.displayName = 'Input';
