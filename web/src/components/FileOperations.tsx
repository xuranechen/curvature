import React, { useCallback, useEffect, useRef, useState } from "react";
import { useI18n } from "../i18n";
import { protectedJSON, ProtectedAPIError } from "../services/api";
import { appURL } from "../services/base";
import { reportError } from "../services/error";
import { LocalPanel, type LocalDirBrowserState } from "./ProjectAddPopover";

export type FileActionOutcome = {
  action: "delete" | "rename" | "move";
  path: string;
  nextPath?: string;
};

const menuItemStyle: React.CSSProperties = {
  width: "100%",
  border: "none",
  background: "transparent",
  color: "var(--text-primary)",
  borderRadius: 8,
  padding: "8px 10px",
  textAlign: "left",
  cursor: "pointer",
  fontSize: 12,
  display: "flex",
  alignItems: "center",
  gap: 8,
};

const iconStyle: React.CSSProperties = {
  width: 16,
  height: 16,
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
  flexShrink: 0,
};

function ActionIcon({ action }: { action: "delete" | "rename" | "move" }) {
  if (action === "delete") {
    return (
      <span aria-hidden="true" style={iconStyle}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <polyline points="3 6 5 6 21 6" />
          <path d="M19 6l-1 14H6L5 6" />
          <path d="M10 11v6" />
          <path d="M14 11v6" />
          <path d="M9 6V4h6v2" />
        </svg>
      </span>
    );
  }
  return (
    <span aria-hidden="true" style={iconStyle}>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        {action === "rename" ? (
          <>
            <path d="M12 20h9" />
            <path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4 12.5-12.5z" />
          </>
        ) : (
          <>
            <path d="M20 10V7a2 2 0 0 0-2-2h-6l-2-2H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h7" />
            <path d="M14 16h8m-3-3 3 3-3 3" />
          </>
        )}
      </svg>
    </span>
  );
}

function MoveDialog({ root, path, onClose, onComplete }: {
  root: string;
  path: string;
  onClose: () => void;
  onComplete: (outcome: FileActionOutcome) => void | Promise<void>;
}) {
  const { t } = useI18n();
  const [dirs, setDirs] = useState<LocalDirBrowserState>({
    path: "",
    items: [],
    loading: true,
    selectedPath: "",
    adding: false,
    error: "",
  });
  const request = useRef(0);
  const busy = useRef(false);

  const browse = useCallback(async (target: string) => {
    if (busy.current) return;
    const id = ++request.current;
    setDirs((prev) => ({ ...prev, loading: true, error: "" }));
    try {
      const result = await protectedJSON<Pick<LocalDirBrowserState, "path" | "parent" | "items" | "volumes">>(
        appURL("/api/local_dirs", new URLSearchParams({ path: target })),
      );
      if (id === request.current) {
        setDirs({ ...result, loading: false, selectedPath: "", adding: false, error: "" });
      }
    } catch (err) {
      if (id === request.current) {
        setDirs((prev) => ({
          ...prev,
          loading: false,
          error: err instanceof Error ? err.message : String(err),
        }));
      }
    }
  }, []);

  useEffect(() => {
    void browse("");
    return () => {
      request.current += 1;
    };
  }, [browse]);

  const submit = async () => {
    if (busy.current || dirs.loading || dirs.error || !dirs.path) return;
    busy.current = true;
    setDirs((prev) => ({ ...prev, adding: true, error: "" }));
    try {
      await protectedJSON(appURL("/api/file/operation"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ root, path, action: "move", destination: dirs.path }),
      });
      await onComplete({ action: "move", path });
    } catch (err) {
      setDirs((prev) => ({
        ...prev,
        error: err instanceof ProtectedAPIError && err.status === 409
          ? t("fileOperation.fileExists")
          : err instanceof Error ? err.message : t("fileOperation.failed"),
      }));
    } finally {
      busy.current = false;
      setDirs((prev) => ({ ...prev, adding: false }));
    }
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={t("fileOperation.moveTitle", { name: path })}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 70,
        background: "rgba(15, 23, 42, 0.28)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: 16,
      }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", color: "#fff", fontSize: 12 }}>
          <span>{t("fileOperation.moveTitle", { name: path.split("/").pop() || path })}</span>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("fileOperation.close")}
            style={{ border: "none", background: "transparent", color: "#fff", cursor: "pointer", fontSize: 18, lineHeight: 1 }}
          >
            ×
          </button>
        </div>
        <LocalPanel
          localState={dirs}
          onLocalNavigate={(target) => void browse(target)}
          onLocalSelect={() => {}}
          onLocalAdd={() => void submit()}
          localActionLabel={t("fileOperation.moveHere")}
          localDisabledAddedRoot={false}
          localBrowseOnly
        />
      </div>
    </div>
  );
}

export function FileActionsButton({ root, path, onComplete }: {
  root: string;
  path: string;
  onComplete: (outcome: FileActionOutcome) => void | Promise<void>;
}) {
  const { t } = useI18n();
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [menuPosition, setMenuPosition] = useState<{ top: number; right: number } | null>(null);
  const [moveOpen, setMoveOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return undefined;
    const close = (event: MouseEvent) => {
      if (buttonRef.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    window.addEventListener("mousedown", close);
    return () => window.removeEventListener("mousedown", close);
  }, [open]);

  const toggle = () => {
    if (open) {
      setOpen(false);
      return;
    }
    const rect = buttonRef.current?.getBoundingClientRect();
    if (!rect) return;
    setMenuPosition({ top: rect.bottom + 6, right: Math.max(8, window.innerWidth - rect.right) });
    setOpen(true);
  };

  const submit = async (action: "delete" | "rename") => {
    if (busy) return;
    let name: string | undefined;
    if (action === "rename") {
      const currentName = path.replace(/\\/g, "/").split("/").pop() || "";
      const input = window.prompt(t("fileOperation.namePrompt"), currentName);
      if (input === null) return;
      name = input.trim();
      if (!name || name === "." || name === ".." || /[/\\\x00]/.test(name)) {
        window.alert(t("fileOperation.invalidName"));
        return;
      }
      if (name === currentName) {
        setOpen(false);
        return;
      }
    } else if (!window.confirm(t("fileOperation.confirmDelete", { name: path }))) {
      return;
    }
    setBusy(true);
    setOpen(false);
    try {
      await protectedJSON(appURL("/api/file/operation"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ root, path, action, name }),
      });
      const parent = path.replace(/\\/g, "/").split("/").slice(0, -1).join("/");
      const nextPath = action === "rename" && name ? `${parent ? `${parent}/` : ""}${name}` : undefined;
      await onComplete({ action, path, nextPath });
    } catch (err) {
      reportError(
        "file.write_failed",
        err instanceof ProtectedAPIError && err.status === 409
          ? t("fileOperation.fileExists")
          : err instanceof Error
            ? err.message
            : t("fileOperation.failed"),
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        onClick={toggle}
        disabled={busy}
        aria-label={t("fileOperation.more")}
        aria-haspopup="menu"
        aria-expanded={open}
        title={t("fileOperation.more")}
        style={{
          border: "none",
          background: open ? "rgba(0, 0, 0, 0.06)" : "transparent",
          borderRadius: 6,
          padding: 0,
          width: 22,
          height: 22,
          display: "inline-flex",
          alignItems: "center",
          justifyContent: "center",
          cursor: busy ? "progress" : "pointer",
          color: "var(--text-secondary)",
          flexShrink: 0,
        }}
      >
        <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
          <circle cx="12" cy="5" r="1.8" />
          <circle cx="12" cy="12" r="1.8" />
          <circle cx="12" cy="19" r="1.8" />
        </svg>
      </button>
      {open && menuPosition ? (
        <div
          role="menu"
          style={{
            position: "fixed",
            top: menuPosition.top,
            right: menuPosition.right,
            zIndex: 50,
            minWidth: 160,
            padding: 6,
            borderRadius: 10,
            border: "1px solid var(--border-color)",
            background: "var(--menu-bg)",
            boxShadow: "0 12px 30px rgba(15, 23, 42, 0.14)",
          }}
        >
          <button
            type="button"
            role="menuitem"
            style={menuItemStyle}
            onClick={() => {
              setOpen(false);
              setMoveOpen(true);
            }}
          >
            <ActionIcon action="move" />
            <span>{t("fileOperation.move")}</span>
          </button>
          <button type="button" role="menuitem" style={menuItemStyle} onClick={() => void submit("rename")}>
            <ActionIcon action="rename" />
            <span>{t("fileOperation.rename")}</span>
          </button>
          <button
            type="button"
            role="menuitem"
            style={{ ...menuItemStyle, color: "#dc2626" }}
            onClick={() => void submit("delete")}
          >
            <ActionIcon action="delete" />
            <span>{t("fileOperation.delete")}</span>
          </button>
        </div>
      ) : null}
      {moveOpen ? (
        <MoveDialog
          root={root}
          path={path}
          onClose={() => setMoveOpen(false)}
          onComplete={async (outcome) => {
            setMoveOpen(false);
            await onComplete(outcome);
          }}
        />
      ) : null}
    </>
  );
}
