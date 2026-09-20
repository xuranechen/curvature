import React from "react";
import { useI18n } from "../i18n";
import {
  DEFAULT_VERSIONING_CHECKPOINT_RETENTION,
  fetchVersioningAgentAutonomyPreference,
  fetchVersioningAutoCheckpointsPreference,
  fetchVersioningCheckpointRetentionPreference,
  updateVersioningAgentAutonomyPreference,
  updateVersioningAutoCheckpointsPreference,
  updateVersioningCheckpointRetentionPreference,
} from "../services/preferences";

type Props = {
  open: boolean;
  onClose: () => void;
};

const overlayStyle: React.CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.42)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  padding: "16px",
  zIndex: 3000,
};

const panelStyle: React.CSSProperties = {
  width: "min(460px, 100%)",
  maxHeight: "min(620px, calc(100vh - 58px))",
  overflow: "auto",
  borderRadius: "12px",
  background: "var(--menu-bg)",
  border: "1px solid var(--border-color)",
  boxShadow: "0 12px 30px rgba(15, 23, 42, 0.14)",
  padding: "14px",
  boxSizing: "border-box",
  display: "flex",
  flexDirection: "column",
  gap: "14px",
};

const groupStyle: React.CSSProperties = { display: "grid", gap: "8px" };

const groupTitleStyle: React.CSSProperties = {
  fontSize: "11px",
  fontWeight: 700,
  color: "var(--text-secondary)",
  letterSpacing: "0.04em",
};

const hintStyle: React.CSSProperties = {
  fontSize: "11px",
  lineHeight: 1.5,
  color: "var(--text-secondary)",
};

const rowStyle: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  gap: "10px",
  borderRadius: "8px",
  border: "1px solid var(--border-color)",
  padding: "8px 10px",
  fontSize: "12px",
  color: "var(--text-primary)",
};

const retentionInputStyle: React.CSSProperties = {
  width: "86px",
  borderRadius: "6px",
  border: "1px solid var(--border-color)",
  background: "transparent",
  color: "var(--text-primary)",
  fontSize: "12px",
  padding: "4px 6px",
  outline: "none",
  boxSizing: "border-box",
};

const closeButtonStyle: React.CSSProperties = {
  width: "26px",
  height: "26px",
  flexShrink: 0,
  borderRadius: "8px",
  border: "none",
  background: "transparent",
  color: "var(--text-secondary)",
  fontSize: "16px",
  lineHeight: 1,
  cursor: "pointer",
  outline: "none",
};

export function VersioningSettingsDialog({ open, onClose }: Props) {
  const { t } = useI18n();
  const [autoCheckpoints, setAutoCheckpoints] = React.useState(true);
  const [agentAutonomy, setAgentAutonomy] = React.useState(false);
  const [retention, setRetention] = React.useState(DEFAULT_VERSIONING_CHECKPOINT_RETENTION);
  const [retentionDraft, setRetentionDraft] = React.useState(
    String(DEFAULT_VERSIONING_CHECKPOINT_RETENTION),
  );
  const [loading, setLoading] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError("");
    Promise.all([
      fetchVersioningAutoCheckpointsPreference().catch(() => null),
      fetchVersioningAgentAutonomyPreference().catch(() => null),
      fetchVersioningCheckpointRetentionPreference().catch(() => null),
    ])
      .then(([checkpoints, autonomy, keep]) => {
        if (cancelled) return;
        if (checkpoints) setAutoCheckpoints(checkpoints.enabled);
        if (autonomy) setAgentAutonomy(autonomy.enabled);
        if (keep) {
          setRetention(keep.keep);
          setRetentionDraft(String(keep.keep));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open]);

  if (!open) return null;

  const locked = busy || loading;

  const toggleAutoCheckpoints = () => {
    if (locked) return;
    const previous = autoCheckpoints;
    const next = !previous;
    setAutoCheckpoints(next);
    setBusy(true);
    setError("");
    updateVersioningAutoCheckpointsPreference(next)
      .then((preference) => setAutoCheckpoints(preference.enabled))
      .catch(() => {
        setAutoCheckpoints(previous);
        setError(t("versioningSettings.saveFailed"));
      })
      .finally(() => setBusy(false));
  };

  const toggleAgentAutonomy = () => {
    if (locked) return;
    const previous = agentAutonomy;
    const next = !previous;
    setAgentAutonomy(next);
    setBusy(true);
    setError("");
    updateVersioningAgentAutonomyPreference(next)
      .then((preference) => setAgentAutonomy(preference.enabled))
      .catch(() => {
        setAgentAutonomy(previous);
        setError(t("versioningSettings.saveFailed"));
      })
      .finally(() => setBusy(false));
  };

  // The server rejects a non-positive count, so an unparseable draft is simply
  // discarded and the last known good value is restored.
  const commitRetention = () => {
    if (locked) return;
    const parsed = Number.parseInt(retentionDraft, 10);
    if (!Number.isFinite(parsed) || parsed <= 0) {
      setRetentionDraft(String(retention));
      return;
    }
    if (parsed === retention) return;
    setBusy(true);
    setError("");
    updateVersioningCheckpointRetentionPreference(parsed)
      .then((preference) => {
        setRetention(preference.keep);
        setRetentionDraft(String(preference.keep));
      })
      .catch(() => {
        setRetentionDraft(String(retention));
        setError(t("versioningSettings.saveFailed"));
      })
      .finally(() => setBusy(false));
  };

  return (
    <div
      style={overlayStyle}
      role="presentation"
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div role="dialog" aria-modal="true" aria-label={t("versioningSettings.title")} style={panelStyle}>
        <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: "10px" }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: "13px", fontWeight: 700, color: "var(--text-primary)" }}>
              {t("versioningSettings.title")}
            </div>
            <div style={hintStyle}>{t("versioningSettings.subtitle")}</div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("versioningSettings.close")}
            title={t("versioningSettings.close")}
            style={closeButtonStyle}
          >
            ×
          </button>
        </div>

        {error ? (
          <div style={{ ...hintStyle, color: "var(--danger-color, #dc2626)" }}>{error}</div>
        ) : null}
        {loading ? <div style={hintStyle}>{t("versioningSettings.loading")}</div> : null}

        <div style={groupStyle}>
          <div style={groupTitleStyle}>{t("versioningSettings.groupCheckpoints")}</div>
          <button
            type="button"
            disabled={locked}
            onClick={toggleAutoCheckpoints}
            style={{
              ...rowStyle,
              background: autoCheckpoints ? "var(--selection-bg)" : "transparent",
              color: autoCheckpoints ? "var(--accent-color)" : "var(--text-primary)",
              cursor: locked ? "default" : "pointer",
              opacity: busy ? 0.65 : 1,
            }}
          >
            <span>{t("fileTree.versioningAutoCheckpoints")}</span>
            <span style={{ fontSize: "11px", opacity: autoCheckpoints ? 1 : 0 }}>✓</span>
          </button>
          <div style={hintStyle}>{t("versioningSettings.autoCheckpointsHint")}</div>
          <div style={rowStyle}>
            <span style={{ flex: 1 }}>{t("versioningSettings.retentionLabel")}</span>
            <input
              type="number"
              min={1}
              inputMode="numeric"
              value={retentionDraft}
              disabled={locked}
              onChange={(event) => setRetentionDraft(event.target.value)}
              onBlur={commitRetention}
              onKeyDown={(event) => {
                if (event.key === "Enter") event.currentTarget.blur();
              }}
              style={retentionInputStyle}
            />
            <span style={{ color: "var(--text-secondary)", fontSize: "11px" }}>
              {t("versioningSettings.retentionUnit")}
            </span>
          </div>
          <div style={hintStyle}>{t("versioningSettings.retentionHint")}</div>
        </div>

        <div style={groupStyle}>
          <div style={groupTitleStyle}>{t("versioningSettings.groupAgent")}</div>
          <button
            type="button"
            disabled={locked}
            onClick={toggleAgentAutonomy}
            style={{
              ...rowStyle,
              background: agentAutonomy ? "var(--selection-bg)" : "transparent",
              color: agentAutonomy ? "var(--accent-color)" : "var(--text-primary)",
              cursor: locked ? "default" : "pointer",
              opacity: busy ? 0.65 : 1,
            }}
          >
            <span>{t("fileTree.versioningAgentAutonomy")}</span>
            <span style={{ fontSize: "11px", opacity: agentAutonomy ? 1 : 0 }}>✓</span>
          </button>
          <div style={hintStyle}>{t("versioningSettings.agentHint")}</div>
        </div>

        <div style={groupStyle}>
          <div style={groupTitleStyle}>{t("versioningSettings.groupStorage")}</div>
          <div style={{ ...rowStyle, fontSize: "11px", color: "var(--text-secondary)" }}>
            <span style={{ color: "var(--text-primary)" }}>{t("versioningSettings.storageLocation")}</span>
            <span>{t("versioningSettings.storageLocationValue")}</span>
          </div>
          <div style={hintStyle}>{t("versioningSettings.storageHint")}</div>
        </div>

        <div style={groupStyle}>
          <div style={groupTitleStyle}>{t("versioningSettings.groupIgnore")}</div>
          <div style={{ ...rowStyle, fontSize: "11px", color: "var(--text-secondary)", wordBreak: "break-all" }}>
            {t("versioningSettings.ignoreSources")}
          </div>
          <div style={hintStyle}>{t("versioningSettings.ignoreHint")}</div>
        </div>
      </div>
    </div>
  );
}
