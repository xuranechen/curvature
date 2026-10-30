import React, { useEffect, useRef, useState } from "react";
import { useI18n } from "../i18n";
import { copyText } from "../services/clipboard";
import { sessionService } from "../services/session";
import type { SessionItem } from "./SessionList";
import {
  loadHandoffTemplate,
  persistHandoffTemplate,
  renderHandoffPack,
  type HandoffContext,
} from "../services/handoff";

type HandoffPackDialogProps = {
  session: SessionItem | null;
  onClose: () => void;
  onInsert: (pack: string) => void;
};

const overlayStyle: React.CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.32)",
  zIndex: 3000,
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  padding: "16px",
};

const dialogStyle: React.CSSProperties = {
  width: "min(760px, 100%)",
  maxHeight: "86vh",
  background: "var(--menu-bg)",
  border: "1px solid var(--border-color)",
  borderRadius: "10px",
  boxShadow: "0 24px 60px rgba(15, 23, 42, 0.24)",
  display: "flex",
  flexDirection: "column",
  overflow: "hidden",
};

const actionButtonStyle: React.CSSProperties = {
  border: "1px solid var(--border-color)",
  borderRadius: "8px",
  padding: "8px 14px",
  background: "transparent",
  color: "var(--text-primary)",
  fontSize: "12px",
  cursor: "pointer",
  whiteSpace: "nowrap",
};

const primaryButtonStyle: React.CSSProperties = {
  ...actionButtonStyle,
  background: "var(--accent-color)",
  borderColor: "var(--accent-color)",
  color: "#fff",
  fontWeight: 600,
};

const dialogScopedStyle = `
  .curvature-handoff-textarea:focus {
    border-color: var(--accent-color) !important;
    box-shadow: none;
  }
  @media (max-width: 640px) {
    .curvature-handoff-overlay {
      top: 36px !important;
      align-items: flex-start !important;
      padding: 8px 12px 12px !important;
    }
    .curvature-handoff-dialog {
      width: 100% !important;
      max-height: calc(100dvh - 56px) !important;
    }
    .curvature-handoff-textarea {
      min-height: 120px !important;
    }
  }
`;

export function HandoffPackDialog({ session, onClose, onInsert }: HandoffPackDialogProps) {
  const { t } = useI18n();
  const [context, setContext] = useState<HandoffContext | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [pack, setPack] = useState("");
  const [copied, setCopied] = useState(false);
  const [editingTemplate, setEditingTemplate] = useState(false);
  const [templateDraft, setTemplateDraft] = useState("");
  const [templateSaveBlocked, setTemplateSaveBlocked] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const requestSeqRef = useRef(0);

  useEffect(() => {
    const seq = ++requestSeqRef.current;
    if (!session?.root_id) {
      return;
    }
    setContext(null);
    setLoadError(false);
    setPack("");
    setCopied(false);
    setEditingTemplate(false);
    setTemplateDraft("");
    setTemplateSaveBlocked(false);
    const sessionKey = session.session_key || session.key;
    sessionService
      .getSessionLogPath(session.root_id, sessionKey)
      .then(({ path, agentSessionId }) => {
        if (requestSeqRef.current !== seq) return;
        const nextContext: HandoffContext = { sessionKey, jsonlPath: path, agentSessionId };
        setContext(nextContext);
        setPack(renderHandoffPack(loadHandoffTemplate(), nextContext));
      })
      .catch((err) => {
        if (requestSeqRef.current !== seq) return;
        console.error("[Handoff] Failed to fetch session log path:", err);
        setLoadError(true);
        const fallbackContext: HandoffContext = { sessionKey, jsonlPath: "", agentSessionId: "" };
        setContext(fallbackContext);
        setPack(renderHandoffPack(loadHandoffTemplate(), fallbackContext));
      });
    return () => {
      requestSeqRef.current += 1;
    };
  }, [session]);

  useEffect(() => {
    if (!session) return undefined;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [session, onClose]);

  useEffect(() => {
    if (context && textareaRef.current) {
      textareaRef.current.focus();
      textareaRef.current.select();
    }
  }, [context, editingTemplate]);

  const handleSaveTemplate = () => {
    if (!context) return;
    if (!templateDraft.includes("{jsonl_path}") || !templateDraft.includes("{session_key}")) {
      setTemplateSaveBlocked(true);
      return;
    }
    persistHandoffTemplate(templateDraft);
    setPack(renderHandoffPack(templateDraft, context));
    setCopied(false);
    setTemplateSaveBlocked(false);
    setEditingTemplate(false);
  };

  if (!session) {
    return null;
  }
  const sessionKey = session.session_key || session.key;
  const title = t("session.handoff.sessionTitle", {
    name: session.name || `Session ${sessionKey.slice(0, 8)}`,
  });

  return (
    <div
      className="curvature-handoff-overlay"
      style={overlayStyle}
      onMouseDown={(event) => {
        if (event.button === 0) onClose();
      }}
    >
      <style>{dialogScopedStyle}</style>
      <div
        className="curvature-handoff-dialog"
        style={dialogStyle}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div
          style={{
            height: "48px",
            padding: "0 16px",
            borderBottom: "1px solid var(--border-color)",
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            gap: "10px",
            flexShrink: 0,
          }}
        >
          <div
            title={title}
            style={{ minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontSize: "15px", fontWeight: 700, color: "var(--text-primary)" }}
          >
            {title}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("common.close")}
            style={{ border: "none", background: "transparent", color: "var(--text-secondary)", cursor: "pointer", fontSize: "20px", lineHeight: 1 }}
          >
            ×
          </button>
        </div>

        <div style={{ flex: 1, minHeight: 0, padding: "12px 16px", display: "flex", flexDirection: "column" }}>
          {!context ? (
            <div style={{ fontSize: "12px", color: "var(--text-secondary)", padding: "18px 0" }}>
              {loadError ? t("session.handoff.pathFailed") : t("session.handoff.loading")}
            </div>
          ) : null}
          <textarea
            ref={textareaRef}
            value={editingTemplate ? templateDraft : pack}
            aria-label={editingTemplate ? t("session.handoff.editTemplate") : title}
            className="curvature-handoff-textarea"
            onChange={(event) => {
              if (editingTemplate) setTemplateDraft(event.target.value);
              else setPack(event.target.value);
              setCopied(false);
              setTemplateSaveBlocked(false);
            }}
            spellCheck={false}
            style={{
              flex: 1,
              minHeight: "180px",
              resize: "vertical",
              border: "1px solid var(--border-color)",
              width: "100%",
              boxSizing: "border-box",
              borderRadius: "8px",
              padding: "8px 10px",
              background: "var(--content-bg, #fff)",
              color: "var(--text-primary)",
              fontSize: "13px",
              lineHeight: 1.65,
              fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
              outline: "none",
            }}
          />
          {editingTemplate ? (
            <div style={{ fontSize: "11px", color: "var(--text-secondary)", padding: "8px 2px 0", lineHeight: 1.5 }}>
              {templateSaveBlocked ? (
                <span style={{ color: "#dc2626" }}>{t("session.handoff.templateInvalid")}</span>
              ) : (
                t("session.handoff.templateHint")
              )}
            </div>
          ) : null}
        </div>

        <div
          style={{
            padding: "10px 16px 14px",
            borderTop: "1px solid var(--border-color)",
            display: "flex",
            alignItems: "center",
            gap: "8px",
            flexWrap: "wrap",
            flexShrink: 0,
          }}
        >
          {editingTemplate ? (
            <>
              <button type="button" onClick={() => { setEditingTemplate(false); setTemplateSaveBlocked(false); }} style={actionButtonStyle}>
                {t("session.handoff.back")}
              </button>
              <div style={{ flex: 1 }} />
              <button type="button" disabled={!context} onClick={handleSaveTemplate} style={primaryButtonStyle}>
                {t("fileViewer.saveFile")}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                disabled={!context}
                onClick={() => {
                  setTemplateDraft(loadHandoffTemplate());
                  setTemplateSaveBlocked(false);
                  setEditingTemplate(true);
                }}
                style={actionButtonStyle}
              >
                {t("session.handoff.editTemplate")}
              </button>
              <div style={{ flex: 1 }} />
              <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                <button
                  type="button"
                  disabled={!context}
                  onClick={() => {
                    void copyText(pack)
                      .then(() => setCopied(true))
                      .catch((err) => console.error("[Handoff] Failed to copy pack:", err));
                  }}
                  style={actionButtonStyle}
                >
                  {copied ? t("session.handoff.copied") : t("session.handoff.copy")}
                </button>
                <button
                  type="button"
                  disabled={!context}
                  onClick={() => {
                    onInsert(pack);
                    onClose();
                  }}
                  style={primaryButtonStyle}
                >
                  {t("session.handoff.insert")}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
