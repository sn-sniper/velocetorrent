import React from 'react';

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  size?: 'xs' | 'sm' | 'md';
}

export const Button: React.FC<ButtonProps> = ({
  variant = 'secondary',
  size = 'sm',
  className = '',
  children,
  disabled,
  ...props
}) => {
  const base =
    'inline-flex items-center justify-center font-mono uppercase tracking-wider transition-colors border select-none cursor-pointer disabled:opacity-40 disabled:pointer-events-none';

  const variants: Record<NonNullable<ButtonProps['variant']>, string> = {
    primary:
      'bg-emerald-500/15 border-emerald-500 text-emerald-400 hover:bg-emerald-500/25 active:bg-emerald-500/35',
    secondary:
      'bg-zinc-900 border-zinc-800 text-zinc-200 hover:border-zinc-600 hover:text-zinc-50 active:bg-zinc-800',
    ghost:
      'bg-transparent border-transparent text-zinc-400 hover:text-zinc-100 hover:bg-zinc-900',
    danger:
      'bg-red-500/10 border-red-500/60 text-red-400 hover:bg-red-500/20 hover:border-red-400',
  };

  const sizes: Record<NonNullable<ButtonProps['size']>, string> = {
    xs: 'px-2 py-1 text-[11px] leading-none',
    sm: 'px-3 py-1.5 text-xs leading-none',
    md: 'px-4 py-2 text-xs leading-none',
  };

  return (
    <button
      className={`${base} ${variants[variant]} ${sizes[size]} ${className}`}
      disabled={disabled}
      {...props}
    >
      {children}
    </button>
  );
};
