import { Component, type ErrorInfo, type ReactNode } from "react";

interface ErrorBoundaryProps {
  readonly children: ReactNode;
  readonly fallback?: (error: Error, reset: () => void) => ReactNode;
}

interface ErrorBoundaryState {
  readonly error: Error | null;
}

/**
 * Root-level React error boundary.
 *
 * Catches render-time exceptions thrown by descendant components and
 * renders a recovery surface. Without this, an uncaught error blanks
 * the admin UI to a white screen.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // eslint-disable-next-line no-console -- intentional: operator-visible error log
    console.error("[admin-web] Unhandled render error:", error, info.componentStack);
  }

  private readonly reset = (): void => {
    this.setState({ error: null });
  };

  render(): ReactNode {
    const { error } = this.state;
    if (error !== null) {
      if (this.props.fallback) {
        return this.props.fallback(error, this.reset);
      }
      return (
        <div role="alert" style={errorScreenStyle}>
          <h1 style={{ margin: 0, fontSize: 20 }}>Admin shell failed to render</h1>
          <pre style={errorMessageStyle}>{error.message}</pre>
          <button type="button" onClick={this.reset} style={buttonStyle}>
            Retry
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}

const errorScreenStyle: React.CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  padding: 24,
  maxWidth: 720,
  margin: "10vh auto",
  border: "1px solid #b91c1c",
  borderRadius: 6,
  background: "#fff1f2",
  color: "#7f1d1d",
};

const errorMessageStyle: React.CSSProperties = {
  whiteSpace: "pre-wrap",
  background: "#fee2e2",
  padding: 12,
  borderRadius: 4,
  fontSize: 13,
  margin: "12px 0",
};

const buttonStyle: React.CSSProperties = {
  padding: "6px 14px",
  border: "1px solid #7f1d1d",
  background: "#fff",
  color: "#7f1d1d",
  borderRadius: 4,
  cursor: "pointer",
};
