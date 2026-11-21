import React from 'react';
import ReactDOM from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from './App';
import './index.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      staleTime: 250,
    },
  },
});

class RootErrorBoundary extends React.Component<
  { children: React.ReactNode },
  { error: Error | null }
> {
  constructor(props: { children: React.ReactNode }) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div className="min-h-screen bg-zinc-950 text-zinc-100 p-8 font-mono">
          <div className="max-w-2xl mx-auto border border-red-500/70 bg-zinc-900 p-6 flex flex-col gap-3">
            <div className="text-xs font-bold text-red-400 uppercase tracking-wider">
              UI RUNTIME FAULT // EXPLICIT ERROR BOUNDARY
            </div>
            <pre className="text-xs text-zinc-300 whitespace-pre-wrap bg-zinc-950 p-3 border border-zinc-800">
              {this.state.error.message}
            </pre>
            <button
              onClick={() => window.location.reload()}
              className="self-start px-3 py-1.5 bg-zinc-950 border border-zinc-700 text-xs text-zinc-200 hover:border-emerald-500 cursor-pointer"
            >
              RELOAD INTERFACE
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <RootErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </RootErrorBoundary>
  </React.StrictMode>
);
