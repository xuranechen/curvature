import React, { Component, type ReactNode } from "react";
import { translateNow, useI18n } from "../i18n";

type ErrorBoundaryProps = {
  children: ReactNode;
  fallback?: ReactNode;
  onError?: (error: Error, errorInfo: React.ErrorInfo) => void;
  name?: string;
  labels?: {
    retry: string;
    unknownError: string;
    genericTitle: string;
    titleWithName: (name: string) => string;
  };
};

type ErrorBoundaryState = {
  hasError: boolean;
  error: Error | null;
};

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo): void {
    const { onError, name } = this.props;

    // Log error
    console.error(`[ErrorBoundary${name ? `:${name}` : ""}]`, error, errorInfo);

    // Call error handler
    onError?.(error, errorInfo);
  }

  handleRetry = (): void => {
    this.setState({ hasError: false, error: null });
  };

  render(): ReactNode {
    const { hasError, error } = this.state;
    const { children, fallback, name } = this.props;
    const labels = this.props.labels || {
      retry: translateNow("common.retry"),
      unknownError: translateNow("common.unknownError"),
      genericTitle: translateNow("error.boundary.genericTitle"),
      titleWithName: (value: string) => translateNow("error.boundary.title", { name: value }),
    };

    if (hasError) {
      if (fallback) {
        return fallback;
      }

      return (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            alignItems: "center",
            justifyContent: "center",
            padding: "40px 20px",
            textAlign: "center",
            background: "rgba(239, 68, 68, 0.05)",
            borderRadius: "12px",
            margin: "20px",
          }}
        >
          <div
            style={{
              fontSize: "48px",
              marginBottom: "16px",
            }}
          >
            ⚠️
          </div>
          <div
            style={{
              fontSize: "16px",
              fontWeight: 600,
              color: "var(--text-primary)",
              marginBottom: "8px",
            }}
          >
            {name ? labels.titleWithName(name) : labels.genericTitle}
          </div>
          <div
            style={{
              fontSize: "13px",
              color: "var(--text-secondary)",
              marginBottom: "20px",
              maxWidth: "400px",
            }}
          >
            {error?.message || labels.unknownError}
          </div>
          <button
            type="button"
            onClick={this.handleRetry}
            style={{
              padding: "10px 20px",
              borderRadius: "8px",
              border: "none",
              background: "#3b82f6",
              color: "#fff",
              fontSize: "13px",
              fontWeight: 500,
              cursor: "pointer",
            }}
          >
            {labels.retry}
          </button>
        </div>
      );
    }

    return children;
  }
}

function LocalizedErrorBoundary(props: ErrorBoundaryProps): React.ReactElement {
  const { t } = useI18n();
  return (
    <ErrorBoundary
      {...props}
      labels={{
        retry: t("common.retry"),
        unknownError: t("common.unknownError"),
        genericTitle: t("error.boundary.genericTitle"),
        titleWithName: (name) => t("error.boundary.title", { name }),
      }}
    />
  );
}

// Specialized error boundaries
export function MainViewErrorBoundary({ children }: { children: ReactNode }): React.ReactElement {
  const { t } = useI18n();
  return (
    <LocalizedErrorBoundary
      name={t("error.boundary.mainView")}
      onError={(error) => {
        // Could send to audit log here
        console.error("[MainView Error]", error);
      }}
    >
      {children}
    </LocalizedErrorBoundary>
  );
}

export function DrawerPanelErrorBoundary({ children }: { children: ReactNode }): React.ReactElement {
  const { t } = useI18n();
  return (
    <LocalizedErrorBoundary
      name={t("error.boundary.drawer")}
      onError={(error) => {
        console.error("[DrawerPanel Error]", error);
      }}
    >
      {children}
    </LocalizedErrorBoundary>
  );
}
