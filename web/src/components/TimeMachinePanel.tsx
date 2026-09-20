import React, { useCallback, useEffect, useMemo, useState } from "react";
import type { GitDiffPayload } from "../services/git";
import { GitDiffViewer } from "./GitDiffViewer";
import { useI18n } from "../i18n";
import type { MessageKey } from "../i18n/types";
import {
  VERSIONING_WORKTREE,
  checkoutVersioningTimeline,
  createVersioningBranch,
  createVersioningSnapshot,
  createVersioningTag,
  getVersioningBranches,
  getVersioningChanges,
  getVersioningCheckpoints,
  getVersioningFileDiff,
  getVersioningFile,
  getVersioningInfo,
  getVersioningLog,
  getVersioningStatus,
  getVersioningTimeline,
  pruneVersioningCheckpoints,
  restoreVersioning,
  runVersioningGC,
  verifyVersioning,
  type VersioningBranchItem,
  type VersioningChangeItem,
  type VersioningCommit,
  type VersioningDiffResult,
  type VersioningRepoInfo,
  type VersioningRevisionInfo,
  type VersioningStatus,
} from "../services/versioning";

/** A historical revision of one file, for plugin-based preview. */
export type HistoricalFilePreview = {
  path: string;
  revision: string;
  content: string;
  binary: boolean;
  truncated: boolean;
  size: number;
};

type TimeMachinePanelProps = {
  rootId: string;
  rootName?: string;
  /** Called after the working tree changed, so the file tree can refresh. */
  onWorkingTreeChanged?: () => void;
  /**
   * Renders a historical revision with the same view plugins the live file
   * browser uses. The host supplies it so the plugin pipeline stays in one
   * place; without it the panel falls back to plain text.
   */
  renderHistoricalFile?: (file: HistoricalFilePreview) => React.ReactNode;
  /** Opens the shared time machine settings dialog owned by the host. */
  onOpenSettings?: () => void;
};

type Tab = "snapshots" | "checkpoints" | "file";

const MAX_TIMELINE_REVISIONS = 50;

function humanBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

function statusKey(status: string): MessageKey {
  switch (status) {
    case "added":
      return "timeMachine.statusAdded";
    case "deleted":
      return "timeMachine.statusDeleted";
    default:
      return "timeMachine.statusModified";
  }
}

function statusColor(status: string): string {
  switch (status) {
    case "added":
      return "#15803d";
    case "deleted":
      return "#b91c1c";
    default:
      return "#b45309";
  }
}

/** Maps a versioning status onto the git status codes GitDiffViewer expects. */
function gitStatusCode(status: string): string {
  switch (status) {
    case "added":
      return "A";
    case "deleted":
      return "D";
    default:
      return "M";
  }
}

function toGitDiffPayload(diff: VersioningDiffResult): GitDiffPayload {
  return {
    path: diff.path,
    status: gitStatusCode(diff.status),
    additions: diff.additions,
    deletions: diff.deletions,
    content: diff.content,
    source: "commit",
  };
}

function formatWhen(value: string, locale: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(locale === "zh-CN" ? "zh-CN" : "en-US", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function TimeMachinePanel({
  rootId,
  rootName,
  onWorkingTreeChanged,
  renderHistoricalFile,
  onOpenSettings,
}: TimeMachinePanelProps) {
  const { t, locale } = useI18n();

  const [tab, setTab] = useState<Tab>("snapshots");
  const [info, setInfo] = useState<VersioningRepoInfo | null>(null);
  const [status, setStatus] = useState<VersioningStatus | null>(null);
  const [branches, setBranches] = useState<VersioningBranchItem[]>([]);
  const [commits, setCommits] = useState<VersioningCommit[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [selectedHash, setSelectedHash] = useState<string | null>(null);
  // Automatic checkpoints are listed separately from the user's snapshots.
  const [checkpoints, setCheckpoints] = useState<VersioningCommit[]>([]);
  const [checkpointsTotal, setCheckpointsTotal] = useState(0);
  const [checkpointsLoading, setCheckpointsLoading] = useState(false);
  const [changes, setChanges] = useState<VersioningChangeItem[]>([]);
  const [changesLoading, setChangesLoading] = useState(false);
  const [compareWorktree, setCompareWorktree] = useState(false);

  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [diff, setDiff] = useState<VersioningDiffResult | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [sideBySide, setSideBySide] = useState(false);

  const [filePath, setFilePath] = useState("");
  const [timelinePath, setTimelinePath] = useState<string | null>(null);
  const [revisions, setRevisions] = useState<VersioningRevisionInfo[]>([]);
  // Content preview for the selected revision, optionally rendered by the view
  // plugins the host injects.
  const [selectedRevision, setSelectedRevision] = useState<VersioningRevisionInfo | null>(null);
  const [historicalPreview, setHistoricalPreview] = useState<HistoricalFilePreview | null>(null);
  const [pluginPreview, setPluginPreview] = useState<React.ReactNode>(null);
  const [previewError, setPreviewError] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [showPreview, setShowPreview] = useState(false);  const [timelineLoading, setTimelineLoading] = useState(false);

  // A checkpoint can be selected from either list, so resolve the selection
  // against both and keep one detail view for both kinds of entry.
  const allCommits = useMemo(
    () => [...commits, ...checkpoints],
    [commits, checkpoints],
  );

  const selectedCommit = useMemo(
    () => allCommits.find((commit) => commit.hash === selectedHash) ?? null,
    [allCommits, selectedHash],
  );

  const baseRevision = useMemo(() => {
    if (!selectedCommit) return "";
    return selectedCommit.parents?.[0] ?? "";
  }, [selectedCommit]);

  const reload = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [nextInfo, nextStatus, log, nextBranches] = await Promise.all([
        getVersioningInfo(rootId),
        getVersioningStatus(rootId, 200),
        getVersioningLog(rootId, { limit: 100 }),
        getVersioningBranches(rootId),
      ]);
      setInfo(nextInfo);
      setStatus(nextStatus);
      setCommits(log.commits);
      setBranches(nextBranches);
      setSelectedHash((current) => {
        if (current && log.commits.some((commit) => commit.hash === current)) return current;
        return log.commits[0]?.hash ?? null;
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [rootId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const loadCheckpoints = useCallback(async () => {
    setCheckpointsLoading(true);
    try {
      const payload = await getVersioningCheckpoints(rootId, 200);
      setCheckpoints(payload.commits);
      setCheckpointsTotal(payload.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setCheckpointsLoading(false);
    }
  }, [rootId]);

  // Checkpoints are only fetched when their tab is opened; they are not part of
  // the project's narrative history and most sessions never look at them.
  useEffect(() => {
    if (tab !== "checkpoints") return;
    void loadCheckpoints();
  }, [tab, loadCheckpoints]);

  // Load the changed files whenever the selected snapshot changes.
  useEffect(() => {
    let cancelled = false;
    if (!selectedCommit) {
      setChanges([]);
      setSelectedPath(null);
      setDiff(null);
      return () => {
        cancelled = true;
      };
    }
    setChangesLoading(true);
    const to = compareWorktree && selectedCommit.hash === status?.head ? VERSIONING_WORKTREE : selectedCommit.hash;
    getVersioningChanges(rootId, baseRevision, to)
      .then((payload) => {
        if (cancelled) return;
        setChanges(payload.changes);
        setSelectedPath(null);
        setDiff(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setChangesLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [rootId, selectedCommit, baseRevision, compareWorktree, status?.head]);

  const openDiff = useCallback(
    async (path: string) => {
      if (!selectedCommit) return;
      setSelectedPath(path);
      setDiff(null);
      setDiffLoading(true);
      try {
        const to = compareWorktree && selectedCommit.hash === status?.head ? VERSIONING_WORKTREE : selectedCommit.hash;
        const result = await getVersioningFileDiff(rootId, { from: baseRevision, to, path });
        setDiff(result);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setDiffLoading(false);
      }
    },
    [rootId, selectedCommit, baseRevision, compareWorktree, status?.head],
  );

  const handleSnapshot = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      const result = await createVersioningSnapshot(rootId, { originKind: "manual" });
      await reload();
      setSelectedHash(result.commit.hash);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [rootId, reload]);

  const handleRestore = useCallback(
    async (hash: string) => {
      if (!window.confirm(t("timeMachine.restoreConfirm"))) return;
      setBusy(true);
      setError(null);
      try {
        const result = await restoreVersioning(rootId, { revision: hash });
        await reload();
        onWorkingTreeChanged?.();
        if (result.safety_commit) {
          setError(null);
          window.alert(t("timeMachine.restoreDone", { hash: result.safety_commit.slice(0, 8) }));
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setBusy(false);
      }
    },
    [rootId, reload, onWorkingTreeChanged, t],
  );

  // Timeline switching has always existed in the engine and the HTTP API; these
  // two handlers are what make it reachable from the UI.
  const handleCheckoutTimeline = useCallback(
    async (name: string) => {
      const target = (name || "").trim();
      if (!target || target === status?.branch) return;
      if (!window.confirm(t("timeMachine.checkoutConfirm", { name: target }))) return;
      setBusy(true);
      setError(null);
      try {
        await checkoutVersioningTimeline(rootId, target);
        await reload();
        onWorkingTreeChanged?.();
        window.alert(t("timeMachine.checkoutDone", { name: target }));
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setBusy(false);
      }
    },
    [rootId, reload, onWorkingTreeChanged, status?.branch, t],
  );

  const handleCreateTimeline = useCallback(async () => {
    const name = window.prompt(t("timeMachine.newTimelinePrompt"))?.trim();
    if (!name) return;
    setBusy(true);
    setError(null);
    try {
      await createVersioningBranch(rootId, name);
      await reload();
      window.alert(t("timeMachine.timelineCreated", { name }));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [rootId, reload, t]);

  const handleTag = useCallback(
    async (hash: string) => {
      const name = window.prompt(t("timeMachine.tagPrompt"));
      if (!name) return;
      setBusy(true);
      try {
        await createVersioningTag(rootId, name.trim(), hash);
        await reload();
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setBusy(false);
      }
    },
    [rootId, reload, t],
  );

  const handleVerify = useCallback(async () => {
    setBusy(true);
    try {
      await verifyVersioning(rootId);
      window.alert(t("timeMachine.verifyOk"));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [rootId, t]);

  const handleGC = useCallback(async () => {
    setBusy(true);
    try {
      const result = await runVersioningGC(rootId);
      await reload();
      window.alert(t("timeMachine.gcDone", { count: result.removed, bytes: humanBytes(result.bytes_freed) }));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [rootId, reload, t]);

  const handlePruneCheckpoints = useCallback(async () => {
    if (!window.confirm(t("timeMachine.pruneConfirm"))) return;
    setBusy(true);
    setError(null);
    try {
      const payload = await pruneVersioningCheckpoints(rootId);
      if (selectedHash && payload.result.dropped > 0) {
        setSelectedHash(null);
      }
      await loadCheckpoints();
      await reload();
      window.alert(
        t("timeMachine.pruneDone", {
          dropped: payload.result.dropped,
          kept: payload.result.kept,
          bytes: humanBytes(payload.gc.bytes_freed),
        }),
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [rootId, loadCheckpoints, reload, selectedHash, t]);

  const loadTimeline = useCallback(async () => {
    const path = filePath.trim();
    if (!path) return;
    setTimelineLoading(true);
    setError(null);
    try {
      const payload = await getVersioningTimeline(rootId, path, MAX_TIMELINE_REVISIONS);
      setRevisions(payload.revisions);
      setTimelinePath(payload.path);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setTimelineLoading(false);
    }
  }, [rootId, filePath]);

  const previewRevision = useCallback(
    async (revision: VersioningRevisionInfo) => {
      setSelectedPath(timelinePath);
      setSelectedRevision(revision);
      // A new selection invalidates any content preview.
      setShowPreview(false);
      setHistoricalPreview(null);
      setPluginPreview(null);
      setPreviewError("");
      setDiffLoading(true);
      setDiff(null);
      try {
        // An added file has no earlier version, so an empty `from` is the
        // honest comparison; everything else compares against its parent.
        const from = revision.status === "added" ? "" : `${revision.hash}~1`;
        const result = await getVersioningFileDiff(rootId, {
          from,
          to: revision.hash,
          path: timelinePath ?? "",
        });
        setDiff(result);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setDiffLoading(false);
      }
    },
    [rootId, timelinePath],
  );

  const loadHistoricalPreview = useCallback(async () => {
    if (!timelinePath || !selectedRevision) return;
    setPreviewLoading(true);
    setPreviewError("");
    try {
      const payload = await getVersioningFile(rootId, {
        path: timelinePath,
        revision: selectedRevision.hash,
      });
      if (!payload.present) {
        setHistoricalPreview(null);
        setPluginPreview(null);
        setPreviewError(t("timeMachine.previewMissing"));
        return;
      }
      const file: HistoricalFilePreview = {
        path: payload.path,
        revision: payload.revision,
        content: payload.content ?? "",
        binary: payload.binary,
        truncated: payload.truncated,
        size: payload.size,
      };
      setHistoricalPreview(file);
      // A broken plugin must not take the panel down; plain text remains.
      if (renderHistoricalFile && !file.binary) {
        try {
          setPluginPreview(renderHistoricalFile(file));
        } catch {
          setPluginPreview(null);
        }
      } else {
        setPluginPreview(null);
      }
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : String(err));
    } finally {
      setPreviewLoading(false);
    }
  }, [rootId, timelinePath, selectedRevision, renderHistoricalFile, t]);

  const togglePreview = useCallback(() => {
    if (showPreview) {
      setShowPreview(false);
      return;
    }
    setShowPreview(true);
    void loadHistoricalPreview();
  }, [showPreview, loadHistoricalPreview]);

  const isNarrow = useNarrowLayout();
  const isCheckpointTab = tab === "checkpoints";
  const visibleCommits = isCheckpointTab ? checkpoints : commits;

  const headerStats = useMemo(() => {
    if (!info) return "";
    return t("timeMachine.stats", {
      commits: info.commits,
      tracked: info.tracked,
      size: humanBytes(info.store_bytes),
    });
  }, [info, t]);

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0, background: "var(--bg-primary)" }}>
      {/* header */}
      <div style={{ padding: "12px 16px", borderBottom: "1px solid var(--border-color)", display: "flex", alignItems: "center", gap: "12px", flexWrap: "wrap" }}>
        <div style={{ display: "flex", flexDirection: "column", gap: "2px", minWidth: 0 }}>
          <div style={{ fontSize: "14px", fontWeight: 600, color: "var(--text-primary)" }}>
            {t("timeMachine.title")}
            {rootName ? <span style={{ color: "var(--text-secondary)", fontWeight: 400 }}> · {rootName}</span> : null}
          </div>
          <div style={{ fontSize: "11px", color: "var(--text-secondary)" }}>
            {t("timeMachine.subtitle")}
            {headerStats ? ` · ${headerStats}` : ""}
          </div>
        </div>

        <div style={{ flex: 1 }} />

        <div style={{ display: "flex", gap: "6px", alignItems: "center", flexWrap: "wrap" }}>
          <TabButton active={tab === "snapshots"} label={t("timeMachine.tabSnapshots")} onClick={() => setTab("snapshots")} />
          <TabButton active={tab === "checkpoints"} label={t("timeMachine.tabCheckpoints")} onClick={() => setTab("checkpoints")} />
          <TabButton active={tab === "file"} label={t("timeMachine.tabFile")} onClick={() => setTab("file")} />
          <ActionButton label={t("timeMachine.reload")} onClick={() => void reload()} disabled={busy || loading} />
          <ActionButton label={t("timeMachine.snapshotNow")} onClick={() => void handleSnapshot()} disabled={busy} primary />
          {onOpenSettings ? (
            <button
              type="button"
              onClick={onOpenSettings}
              aria-label={t("fileTree.versioningSettings")}
              title={t("fileTree.versioningSettings")}
              style={{
                width: "28px",
                height: "28px",
                borderRadius: "8px",
                border: "1px solid var(--border-color)",
                background: "transparent",
                color: "var(--text-secondary)",
                cursor: "pointer",
                display: "inline-flex",
                alignItems: "center",
                justifyContent: "center",
                fontSize: "14px",
                lineHeight: 1,
                outline: "none",
              }}
            >
              ⚙
            </button>
          ) : null}
        </div>
      </div>

      {status ? (
        <div style={{ padding: "6px 16px", fontSize: "12px", color: status.clean ? "#15803d" : "#b45309", borderBottom: "1px solid var(--border-color)", display: "flex", alignItems: "center", gap: "8px", flexWrap: "wrap" }}>
          <span>
            {status.clean ? t("timeMachine.clean") : t("timeMachine.dirty", { count: status.dirty })}
            {status.head ? <span style={{ color: "var(--text-secondary)" }}> · {status.head.slice(0, 8)}</span> : null}
          </span>
          <div style={{ flex: 1 }} />
          <label style={{ display: "flex", alignItems: "center", gap: "4px", color: "var(--text-secondary)" }}>
            {t("timeMachine.timeline")}
            <select
              value={status.branch || ""}
              disabled={busy || branches.length === 0}
              onChange={(event) => void handleCheckoutTimeline(event.target.value)}
              style={{ fontSize: "12px", padding: "2px 4px", maxWidth: "180px" }}
            >
              {/* A detached head reports a branch the listing may not contain. */}
              {status.branch && !branches.some((branch) => branch.name === status.branch) ? (
                <option value={status.branch}>{status.branch}</option>
              ) : null}
              {branches.map((branch) => (
                <option key={branch.name} value={branch.name}>
                  {branch.current ? `* ${branch.name}` : branch.name}
                </option>
              ))}
            </select>
          </label>
          <ActionButton label={t("timeMachine.newTimeline")} onClick={() => void handleCreateTimeline()} disabled={busy} />
        </div>
      ) : null}

      {error ? (
        <div style={{ padding: "8px 16px", fontSize: "12px", color: "#b91c1c", borderBottom: "1px solid var(--border-color)" }}>
          {t("timeMachine.errorPrefix")}{error}
        </div>
      ) : null}

      {loading ? (
        <div style={{ padding: "24px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
      ) : tab === "snapshots" || tab === "checkpoints" ? (
        <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: isNarrow ? "column" : "row" }}>
          {/* snapshot list */}
          <div style={{ ...narrowPaneStyle(isNarrow, 320), overflowY: "auto" }}>
            {isCheckpointTab && checkpointsLoading ? (
              <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
            ) : visibleCommits.length === 0 ? (
              <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>
                {isCheckpointTab ? t("timeMachine.noCheckpoints") : t("timeMachine.noSnapshots")}
              </div>
            ) : (
              visibleCommits.map((commit) => {
                const active = commit.hash === selectedHash;
                return (
                  <button
                    key={commit.hash}
                    type="button"
                    onClick={() => setSelectedHash(commit.hash)}
                    style={{
                      width: "100%",
                      border: "none",
                      borderBottom: "1px solid var(--border-color)",
                      background: active ? "var(--selection-bg)" : "transparent",
                      color: active ? "var(--accent-color)" : "var(--text-primary)",
                      padding: "10px 12px",
                      textAlign: "left",
                      cursor: "pointer",
                      display: "flex",
                      flexDirection: "column",
                      gap: "4px",
                    }}
                  >
                    <span style={{ fontSize: "12px", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                      {commit.message}
                    </span>
                    <span style={{ fontSize: "11px", color: "var(--text-secondary)", display: "flex", gap: "6px", flexWrap: "wrap" }}>
                      <code>{commit.hash.slice(0, 8)}</code>
                      <span>{formatWhen(commit.created_at, locale)}</span>
                      {commit.origin?.kind ? <OriginBadge kind={commit.origin.kind} /> : null}
                      {commit.tags?.map((tag) => (
                        <span key={tag} style={{ color: "var(--accent-color)" }}>
                          #{tag}
                        </span>
                      ))}
                    </span>
                    <span style={{ fontSize: "11px", color: "var(--text-secondary)", fontVariantNumeric: "tabular-nums" }}>
                      <span style={{ color: "#15803d" }}>+{commit.stats.added}</span>{" "}
                      <span style={{ color: "#b45309" }}>~{commit.stats.modified}</span>{" "}
                      <span style={{ color: "#b91c1c" }}>-{commit.stats.deleted}</span>
                    </span>
                  </button>
                );
              })
            )}

            <div style={{ padding: "10px 12px", display: "flex", gap: "6px", flexWrap: "wrap", alignItems: "center" }}>
              {isCheckpointTab ? (
                <>
                  <span style={{ fontSize: "11px", color: "var(--text-secondary)" }}>
                    {t("timeMachine.checkpointTotal", { total: checkpointsTotal })}
                  </span>
                  <ActionButton
                    label={t("timeMachine.pruneCheckpoints")}
                    onClick={() => void handlePruneCheckpoints()}
                    disabled={busy || checkpointsTotal === 0}
                    danger
                  />
                </>
              ) : (
                <>
                  <ActionButton label={t("timeMachine.verify")} onClick={() => void handleVerify()} disabled={busy} />
                  <ActionButton label={t("timeMachine.gc")} onClick={() => void handleGC()} disabled={busy} />
                </>
              )}
            </div>
          </div>

          {/* detail */}
          <div style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column" }}>
            {!selectedCommit ? (
              <div style={{ padding: "24px", fontSize: "12px", color: "var(--text-secondary)" }}>
                {t("timeMachine.selectSnapshot")}
              </div>
            ) : (
              <>
                <div style={{ padding: "10px 14px", borderBottom: "1px solid var(--border-color)", display: "flex", alignItems: "center", gap: "8px", flexWrap: "wrap" }}>
                  <span style={{ fontSize: "12px", fontWeight: 600, color: "var(--text-primary)" }}>{selectedCommit.message}</span>
                  <code style={{ fontSize: "11px", color: "var(--text-secondary)" }}>{selectedCommit.hash.slice(0, 8)}</code>
                  <div style={{ flex: 1 }} />
                  {selectedCommit.hash === status?.head ? (
                    <label style={{ fontSize: "11px", color: "var(--text-secondary)", display: "flex", alignItems: "center", gap: "4px" }}>
                      <input type="checkbox" checked={compareWorktree} onChange={(event) => setCompareWorktree(event.target.checked)} />
                      {t("timeMachine.compareWorktree")}
                    </label>
                  ) : null}
                  <ActionButton label={t("timeMachine.tag")} onClick={() => void handleTag(selectedCommit.hash)} disabled={busy} />
                  <ActionButton label={t("timeMachine.restore")} onClick={() => void handleRestore(selectedCommit.hash)} disabled={busy} danger />
                </div>

                <div style={{ display: "flex", flex: 1, minHeight: 0, flexDirection: isNarrow ? "column" : "row" }}>
                  <div style={{ ...narrowPaneStyle(isNarrow, 280), overflowY: "auto" }}>
                    <div style={{ padding: "8px 12px", fontSize: "11px", color: "var(--text-secondary)", borderBottom: "1px solid var(--border-color)" }}>
                      {t("timeMachine.changedFiles")}
                    </div>
                    {changesLoading ? (
                      <div style={{ padding: "12px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
                    ) : changes.length === 0 ? (
                      <div style={{ padding: "12px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.noChanges")}</div>
                    ) : (
                      changes.map((change) => {
                        const active = change.path === selectedPath;
                        return (
                          <button
                            key={change.path}
                            type="button"
                            onClick={() => void openDiff(change.path)}
                            style={{
                              width: "100%",
                              border: "none",
                              borderBottom: "1px solid var(--border-color)",
                              background: active ? "var(--selection-bg)" : "transparent",
                              padding: "7px 12px",
                              textAlign: "left",
                              cursor: "pointer",
                              display: "flex",
                              flexDirection: "column",
                              gap: "2px",
                            }}
                          >
                            <span
                              style={{
                                fontSize: "11px",
                                color: "var(--text-primary)",
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                                direction: "rtl",
                                textAlign: "left",
                              }}
                              title={change.path}
                            >
                              {change.path}
                            </span>
                            <span style={{ fontSize: "11px", display: "flex", gap: "6px" }}>
                              <span style={{ color: statusColor(change.status) }}>{t(statusKey(change.status))}</span>
                              {change.binary ? (
                                <span style={{ color: "var(--text-secondary)" }}>{t("timeMachine.binary")}</span>
                              ) : (
                                <>
                                  <span style={{ color: "#15803d" }}>+{change.additions}</span>
                                  <span style={{ color: "#b91c1c" }}>-{change.deletions}</span>
                                </>
                              )}
                            </span>
                          </button>
                        );
                      })
                    )}
                  </div>

                  <div style={{ flex: 1, minWidth: 0, overflow: "auto" }}>
                    {showPreview ? (
                      previewLoading ? (
                        <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
                      ) : previewError ? (
                        <div style={{ padding: "16px", fontSize: "12px", color: "#b91c1c" }}>{previewError}</div>
                      ) : !historicalPreview ? (
                        <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.previewHint")}</div>
                      ) : (
                        <div style={{ padding: "10px 12px" }}>
                          {historicalPreview.truncated ? (
                            <div style={{ fontSize: "11px", color: "var(--text-secondary)", marginBottom: "8px" }}>
                              {t("timeMachine.previewTruncated")}
                            </div>
                          ) : null}
                          {historicalPreview.binary ? (
                            <div style={{ fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.binary")}</div>
                          ) : pluginPreview ? (
                            pluginPreview
                          ) : (
                            <pre style={{ margin: 0, fontSize: "12px", whiteSpace: "pre-wrap", wordBreak: "break-word" }}>
                              {historicalPreview.content}
                            </pre>
                          )}
                        </div>
                      )
                    ) : diffLoading ? (
                      <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
                    ) : !diff ? (
                      <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>
                        {t("timeMachine.selectSnapshot")}
                      </div>
                    ) : diff.binary ? (
                      <div style={{ padding: "16px", fontSize: "12px", color: "var(--text-secondary)" }}>
                        {diff.message || t("timeMachine.binary")}
                      </div>
                    ) : (
                      <div style={{ padding: "10px 12px" }}>
                        <div style={{ display: "flex", alignItems: "center", gap: "8px", marginBottom: "8px" }}>
                          <code style={{ fontSize: "11px", color: "var(--text-secondary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                            {diff.path}
                          </code>
                          <span style={{ fontSize: "11px", color: "#15803d" }}>+{diff.additions}</span>
                          <span style={{ fontSize: "11px", color: "#b91c1c" }}>-{diff.deletions}</span>
                          <div style={{ flex: 1 }} />
                          <ActionButton label={t("timeMachine.sideBySide")} onClick={() => setSideBySide((value) => !value)} disabled={false} />
                          <ActionButton
                            label={showPreview ? t("timeMachine.showDiff") : t("timeMachine.previewContent")}
                            onClick={togglePreview}
                            disabled={false}
                          />
                        </div>
                        <GitDiffViewer diff={toGitDiffPayload(diff)} root={rootName ?? null} sideBySide={sideBySide} />
                      </div>
                    )}
                  </div>
                </div>
              </>
            )}
          </div>
        </div>
      ) : (
        /* file time machine */
        <div style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: isNarrow ? "column" : "row" }}>
          <div style={{ ...narrowPaneStyle(isNarrow, 340), display: "flex", flexDirection: "column" }}>
            <div style={{ padding: "12px", display: "flex", gap: "6px" }}>
              <input
                value={filePath}
                onChange={(event) => setFilePath(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") void loadTimeline();
                }}
                placeholder={t("timeMachine.filePlaceholder")}
                style={{
                  flex: 1,
                  minWidth: 0,
                  background: "var(--bg-secondary)",
                  border: "1px solid var(--border-color)",
                  borderRadius: "6px",
                  color: "var(--text-primary)",
                  padding: "6px 8px",
                  fontSize: "12px",
                }}
              />
              <ActionButton label={t("timeMachine.loadTimeline")} onClick={() => void loadTimeline()} disabled={timelineLoading} primary />
            </div>

            <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {timelineLoading ? (
                <div style={{ padding: "12px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
              ) : revisions.length === 0 ? (
                <div style={{ padding: "12px", fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.noRevisions")}</div>
              ) : (
                revisions.map((revision) => (
                  <button
                    key={`${revision.hash}-${revision.created_at}`}
                    type="button"
                    onClick={() => void previewRevision(revision)}
                    style={{
                      width: "100%",
                      border: "none",
                      borderBottom: "1px solid var(--border-color)",
                      background: "transparent",
                      padding: "9px 12px",
                      textAlign: "left",
                      cursor: "pointer",
                      display: "flex",
                      flexDirection: "column",
                      gap: "3px",
                    }}
                  >
                    <span style={{ fontSize: "12px", color: "var(--text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                      {revision.message}
                    </span>
                    <span style={{ fontSize: "11px", color: "var(--text-secondary)", display: "flex", gap: "6px", flexWrap: "wrap" }}>
                      <code>{revision.hash.slice(0, 8)}</code>
                      <span style={{ color: statusColor(revision.status) }}>{t(statusKey(revision.status))}</span>
                      <span>{formatWhen(revision.created_at, locale)}</span>
                      {revision.binary ? <span>{t("timeMachine.binary")}</span> : null}
                    </span>
                  </button>
                ))
              )}
            </div>
          </div>

          <div style={{ flex: 1, minWidth: 0, overflow: "auto", padding: "12px" }}>
            {diffLoading ? (
              <div style={{ fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.loading")}</div>
            ) : !diff ? (
              <div style={{ fontSize: "12px", color: "var(--text-secondary)" }}>{t("timeMachine.selectSnapshot")}</div>
            ) : diff.binary ? (
              <div style={{ fontSize: "12px", color: "var(--text-secondary)" }}>{diff.message || t("timeMachine.binary")}</div>
            ) : (
              <GitDiffViewer diff={toGitDiffPayload(diff)} root={rootName ?? null} sideBySide={false} />
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function TabButton({ active, label, onClick }: { active: boolean; label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        border: "1px solid var(--border-color)",
        background: active ? "var(--selection-bg)" : "transparent",
        color: active ? "var(--accent-color)" : "var(--text-primary)",
        borderRadius: "6px",
        padding: "5px 10px",
        fontSize: "12px",
        cursor: "pointer",
      }}
    >
      {label}
    </button>
  );
}

// useNarrowLayout tracks the same breakpoint the file viewer and diff viewer
// use, so the time machine stacks its fixed-width sidebars instead of squeezing
// the detail pane to nothing on a phone. Inline styles have no media queries, so
// this is where the breakpoint lives.
function useNarrowLayout(): boolean {
  const [narrow, setNarrow] = useState(
    () => typeof window !== "undefined" && window.matchMedia("(max-width: 767px)").matches,
  );
  useEffect(() => {
    if (typeof window === "undefined") return;
    const media = window.matchMedia("(max-width: 767px)");
    const handleChange = () => setNarrow(media.matches);
    handleChange();
    media.addEventListener("change", handleChange);
    return () => media.removeEventListener("change", handleChange);
  }, []);
  return narrow;
}

// narrowPaneStyle turns a fixed-width sidebar into a stacked one above the
// content, keeping it short enough that the detail pane stays on screen.
function narrowPaneStyle(narrow: boolean, width: number): React.CSSProperties {
  if (!narrow) {
    return { width: `${width}px`, flexShrink: 0, borderRight: "1px solid var(--border-color)" };
  }
  return {
    width: "100%",
    flexShrink: 0,
    maxHeight: "45%",
    borderRight: "none",
    borderBottom: "1px solid var(--border-color)",
  };
}

function ActionButton({
  label,
  onClick,
  disabled,
  primary,
  danger,
}: {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  primary?: boolean;
  danger?: boolean;
}) {
  const color = danger ? "#b91c1c" : primary ? "var(--accent-color)" : "var(--text-primary)";
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      style={{
        border: "1px solid var(--border-color)",
        background: "transparent",
        color,
        borderRadius: "6px",
        padding: "5px 10px",
        fontSize: "12px",
        cursor: disabled ? "default" : "pointer",
        opacity: disabled ? 0.5 : 1,
        whiteSpace: "nowrap",
      }}
    >
      {label}
    </button>
  );
}

function OriginBadge({ kind }: { kind: string }) {
  const { t } = useI18n();
  const key: MessageKey =
    kind === "auto"
      ? "timeMachine.originAuto"
      : kind === "safety"
        ? "timeMachine.originSafety"
        : kind === "session"
          ? "timeMachine.originSession"
          : kind === "task"
            ? "timeMachine.originTask"
            : kind === "revert"
              ? "timeMachine.originRevert"
              : kind === "restore"
                ? "timeMachine.originRestore"
                : "timeMachine.originManual";
  return (
    <span style={{ border: "1px solid var(--border-color)", borderRadius: "4px", padding: "0 4px", color: "var(--text-secondary)" }}>
      {t(key)}
    </span>
  );
}
