import type { UploadProgress } from "../services/upload";

function formatUploadSpeed(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) {
    return "0 KB/s";
  }
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  let value = bytesPerSecond;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  const digits = value >= 100 || index === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits).replace(/\.0+$|(\.\d*[1-9])0+$/, "$1")} ${units[index]}`;
}

export function CompactUploadProgress({
  progress,
  label,
  statusLabel,
  cancelLabel,
  onCancel,
}: {
  progress: UploadProgress | null;
  label: string;
  statusLabel?: string;
  cancelLabel?: string;
  onCancel?: () => void;
}) {
  if (!progress) return null;
  const percent = Math.min(100, Math.max(0, progress.percent));
  return (
    <div
      role="status"
      aria-live="polite"
      aria-label={`${label} ${percent}% ${formatUploadSpeed(progress.speedBps)}`}
      title={`${label} ${percent}% ${formatUploadSpeed(progress.speedBps)}`}
      style={{
        height: "28px",
        display: "inline-flex",
        alignItems: "center",
        gap: "6px",
        padding: "0 5px 0 6px",
        border: "1px solid var(--border-color)",
        borderRadius: "7px",
        background: "var(--panel-bg)",
        color: "var(--text-secondary)",
        fontSize: "10px",
        lineHeight: 1,
        flexShrink: 0,
      }}
    >
      <div
        aria-hidden="true"
        style={{
          width: "18px",
          height: "18px",
          borderRadius: "999px",
          background: `conic-gradient(var(--accent-color) 0 ${percent}%, rgba(148, 163, 184, 0.22) ${percent}% 100%)`,
          position: "relative",
          flexShrink: 0,
        }}
      >
        <span
          style={{
            position: "absolute",
            inset: "4.5px",
            borderRadius: "999px",
            background: "var(--panel-bg)",
          }}
        />
      </div>
      <span
        style={{
          display: "inline-flex",
          flexDirection: "column",
          gap: "1px",
          whiteSpace: "nowrap",
          minWidth: "50px",
          lineHeight: 1.05,
        }}
      >
        <span style={{ color: "var(--text-primary)", fontWeight: 700 }}>
          {statusLabel || label}
        </span>
        <span style={{ fontVariantNumeric: "tabular-nums" }}>
          {formatUploadSpeed(progress.speedBps)}
        </span>
      </span>
      <button
        type="button"
        aria-label={cancelLabel || label}
        title={cancelLabel || label}
        onClick={onCancel}
        style={{
          width: "16px",
          height: "16px",
          border: "none",
          borderRadius: "5px",
          background: "transparent",
          color: "var(--text-secondary)",
          display: "inline-flex",
          alignItems: "center",
          justifyContent: "center",
          padding: 0,
          cursor: onCancel ? "pointer" : "default",
        }}
      >
        ×
      </button>
    </div>
  );
}
