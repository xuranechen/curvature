import { appURL } from "./base";
import { protectedJSON } from "./api";

/**
 * Pseudo-revision that reads a path from the working tree instead of history.
 * Mirrors `versioning.WorktreeRev` on the backend.
 */
export const VERSIONING_WORKTREE = "WORKTREE";

/** Default revision for read-only operations. */
export const VERSIONING_HEAD = "HEAD";

export type VersioningStatusCode = "added" | "modified" | "deleted";

export type VersioningRepoInfo = {
  initialized: boolean;
  root_path: string;
  store_path: string;
  branch?: string;
  head?: string;
  commits: number;
  branches: number;
  tags: number;
  tracked: number;
  objects: number;
  store_bytes: number;
  created_at?: string;
  updated_at?: string;
};

export type VersioningFileStatus = {
  path: string;
  status: VersioningStatusCode | string;
  size?: number;
  binary?: boolean;
};

export type VersioningStatus = {
  initialized: boolean;
  branch?: string;
  head?: string;
  clean: boolean;
  dirty: number;
  total: number;
  truncated?: boolean;
  items: VersioningFileStatus[];
};

export type VersioningOrigin = {
  kind?: string;
  agent?: string;
  session_id?: string;
  task_id?: string;
  label?: string;
  detail?: string;
};

export type VersioningCommitStats = {
  files: number;
  bytes: number;
  added: number;
  modified: number;
  deleted: number;
  skipped: number;
};

export type VersioningCommit = {
  hash: string;
  parents?: string[];
  manifest: string;
  message: string;
  author?: string;
  created_at: string;
  branch?: string;
  origin?: VersioningOrigin;
  tags?: string[];
  stats: VersioningCommitStats;
};

export type VersioningChangeItem = {
  path: string;
  status: VersioningStatusCode | string;
  old_hash?: string;
  new_hash?: string;
  old_size?: number;
  new_size?: number;
  additions: number;
  deletions: number;
  binary?: boolean;
};

export type VersioningDiffResult = {
  path: string;
  status: VersioningStatusCode | string;
  binary?: boolean;
  additions: number;
  deletions: number;
  old_size?: number;
  new_size?: number;
  truncated?: boolean;
  content: string;
  message?: string;
};

export type VersioningRevisionInfo = {
  hash: string;
  message: string;
  author?: string;
  created_at: string;
  status: VersioningStatusCode | string;
  additions: number;
  deletions: number;
  size?: number;
  binary?: boolean;
  origin?: VersioningOrigin;
};

export type VersioningBranchItem = {
  name: string;
  head: string;
  current: boolean;
  subject?: string;
  created_at?: string;
};

export type VersioningTagItem = {
  name: string;
  hash: string;
  created_at?: string;
};

export type VersioningRestoreResult = {
  revision: string;
  branch?: string;
  mode: string;
  restored?: string[];
  deleted?: string[];
  skipped?: string[];
  safety_commit?: string;
  message?: string;
};

export type VersioningTreeEntry = {
  path: string;
  name: string;
  size: number;
  binary?: boolean;
};

export type VersioningCheckpointsPayload = {
  commits: VersioningCommit[];
  total: number;
};

export type VersioningPruneResult = {
  timeline: string;
  kept: number;
  dropped: number;
};

export type VersioningPrunePayload = {
  result: VersioningPruneResult;
  gc: VersioningGCResult;
};

export type VersioningGCResult = {
  removed: number;
  kept: number;
  bytes_freed: number;
};

export type VersioningFilePayload = {
  path: string;
  revision: string;
  present: boolean;
  hash: string;
  size: number;
  binary: boolean;
  truncated: boolean;
  content?: string;
};

export type VersioningSnapshotPayload = {
  commit: VersioningCommit;
  created: boolean;
  reason?: string;
};

export type VersioningSnapshotInput = {
  message?: string;
  author?: string;
  tag?: string;
  originKind?: string;
  sessionId?: string;
  taskId?: string;
  force?: boolean;
};

function versioningQuery(rootId: string, extra?: Record<string, string | number | undefined>): URLSearchParams {
  const params = new URLSearchParams({ root: rootId });
  if (extra) {
    for (const [key, value] of Object.entries(extra)) {
      if (value === undefined || value === "") continue;
      params.set(key, String(value));
    }
  }
  return params;
}

function versioningBody(rootId: string, extra?: Record<string, unknown>): RequestInit {
  return {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ root: rootId, ...extra }),
  };
}

function versioningDeleteBody(rootId: string, extra?: Record<string, unknown>): RequestInit {
  return {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ root: rootId, ...extra }),
  };
}

// ---------------------------------------------------------------------------
// info / status
// ---------------------------------------------------------------------------

export async function getVersioningInfo(rootId: string): Promise<VersioningRepoInfo> {
  const payload = await protectedJSON<{ info: VersioningRepoInfo }>(
    appURL("/api/versioning/info", versioningQuery(rootId)),
  );
  return payload.info;
}

export async function getVersioningStatus(rootId: string, limit?: number): Promise<VersioningStatus> {
  const payload = await protectedJSON<{ status: VersioningStatus }>(
    appURL("/api/versioning/status", versioningQuery(rootId, { limit })),
  );
  return payload.status;
}

// ---------------------------------------------------------------------------
// snapshots
// ---------------------------------------------------------------------------

export async function createVersioningSnapshot(
  rootId: string,
  input: VersioningSnapshotInput = {},
): Promise<VersioningSnapshotPayload> {
  return protectedJSON<VersioningSnapshotPayload>(
    appURL("/api/versioning/snapshots"),
    versioningBody(rootId, {
      message: input.message,
      author: input.author,
      tag: input.tag,
      origin_kind: input.originKind,
      session_id: input.sessionId,
      task_id: input.taskId,
      force: input.force,
    }),
  );
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

export async function getVersioningLog(
  rootId: string,
  options: { limit?: number; before?: string; branch?: string; path?: string } = {},
): Promise<{ commits: VersioningCommit[]; limit: number }> {
  return protectedJSON<{ commits: VersioningCommit[]; limit: number }>(
    appURL(
      "/api/versioning/log",
      versioningQuery(rootId, {
        limit: options.limit,
        before: options.before,
        branch: options.branch,
        path: options.path,
      }),
    ),
  );
}

export async function getVersioningChanges(
  rootId: string,
  from: string,
  to: string,
): Promise<{ changes: VersioningChangeItem[]; from: string; to: string }> {
  return protectedJSON<{ changes: VersioningChangeItem[]; from: string; to: string }>(
    appURL("/api/versioning/changes", versioningQuery(rootId, { from, to })),
  );
}

export async function getVersioningTimeline(
  rootId: string,
  path: string,
  limit?: number,
): Promise<{ path: string; revisions: VersioningRevisionInfo[] }> {
  return protectedJSON<{ path: string; revisions: VersioningRevisionInfo[] }>(
    appURL("/api/versioning/timeline", versioningQuery(rootId, { path, limit })),
  );
}

// ---------------------------------------------------------------------------
// content access
// ---------------------------------------------------------------------------

export async function getVersioningFileDiff(
  rootId: string,
  options: { from: string; to: string; path: string; context?: number },
): Promise<VersioningDiffResult> {
  const payload = await protectedJSON<{ diff: VersioningDiffResult }>(
    appURL(
      "/api/versioning/file/diff",
      versioningQuery(rootId, {
        from: options.from,
        to: options.to,
        path: options.path,
        context: options.context,
      }),
    ),
  );
  return payload.diff;
}

export async function getVersioningFile(
  rootId: string,
  options: { path: string; revision?: string; maxBytes?: number },
): Promise<VersioningFilePayload> {
  return protectedJSON<VersioningFilePayload>(
    appURL(
      "/api/versioning/file",
      versioningQuery(rootId, {
        path: options.path,
        revision: options.revision,
        max_bytes: options.maxBytes,
      }),
    ),
  );
}

export async function getVersioningTree(
  rootId: string,
  options: { revision?: string; dir?: string } = {},
): Promise<{ entries: VersioningTreeEntry[] }> {
  return protectedJSON<{ entries: VersioningTreeEntry[] }>(
    appURL("/api/versioning/tree", versioningQuery(rootId, { revision: options.revision, dir: options.dir })),
  );
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

export async function restoreVersioning(
  rootId: string,
  options: {
    revision: string;
    paths?: string[];
    mode?: "overwrite" | "revert";
    message?: string;
    skipSafety?: boolean;
  },
): Promise<VersioningRestoreResult> {
  const payload = await protectedJSON<{ result: VersioningRestoreResult }>(
    appURL("/api/versioning/restore"),
    versioningBody(rootId, {
      revision: options.revision,
      paths: options.paths,
      mode: options.mode,
      message: options.message,
      skip_safety: options.skipSafety,
    }),
  );
  return payload.result;
}

// ---------------------------------------------------------------------------
// timelines and tags
// ---------------------------------------------------------------------------

export async function getVersioningBranches(rootId: string): Promise<VersioningBranchItem[]> {
  const payload = await protectedJSON<{ branches: VersioningBranchItem[] }>(
    appURL("/api/versioning/branches", versioningQuery(rootId)),
  );
  return payload.branches;
}

export async function createVersioningBranch(
  rootId: string,
  name: string,
  from?: string,
): Promise<{ name: string; from: string }> {
  return protectedJSON<{ name: string; from: string }>(
    appURL("/api/versioning/branches"),
    versioningBody(rootId, { name, from }),
  );
}

export async function checkoutVersioningTimeline(
  rootId: string,
  name: string,
  mode?: "overwrite" | "revert",
): Promise<VersioningRestoreResult> {
  const payload = await protectedJSON<{ result: VersioningRestoreResult }>(
    appURL("/api/versioning/checkout"),
    versioningBody(rootId, { name, mode }),
  );
  return payload.result;
}

export async function getVersioningTags(rootId: string): Promise<VersioningTagItem[]> {
  const payload = await protectedJSON<{ tags: VersioningTagItem[] }>(
    appURL("/api/versioning/tags", versioningQuery(rootId)),
  );
  return payload.tags;
}

export async function createVersioningTag(
  rootId: string,
  name: string,
  revision?: string,
): Promise<{ name: string }> {
  return protectedJSON<{ name: string }>(
    appURL("/api/versioning/tags"),
    versioningBody(rootId, { name, revision }),
  );
}

export async function deleteVersioningTag(rootId: string, name: string): Promise<{ name: string }> {
  return protectedJSON<{ name: string }>(
    appURL("/api/versioning/tags"),
    versioningDeleteBody(rootId, { name }),
  );
}

// ---------------------------------------------------------------------------
// maintenance
// ---------------------------------------------------------------------------

export async function runVersioningGC(
  rootId: string,
  gracePeriodSeconds?: number,
): Promise<VersioningGCResult> {
  const payload = await protectedJSON<{ result: VersioningGCResult }>(
    appURL("/api/versioning/gc"),
    versioningBody(rootId, { grace_period_seconds: gracePeriodSeconds }),
  );
  return payload.result;
}

export async function verifyVersioning(rootId: string): Promise<{ ok: boolean }> {
  return protectedJSON<{ ok: boolean }>(appURL("/api/versioning/verify", versioningQuery(rootId)));
}

// ---------------------------------------------------------------------------
// automatic checkpoints
// ---------------------------------------------------------------------------

/**
 * Automatic checkpoints are kept out of the normal log on purpose: they are
 * safety nets, not part of the project's narrative history.
 */
export async function getVersioningCheckpoints(
  rootId: string,
  limit?: number,
): Promise<VersioningCheckpointsPayload> {
  return protectedJSON<VersioningCheckpointsPayload>(
    appURL("/api/versioning/checkpoints", versioningQuery(rootId, { limit })),
  );
}

/**
 * Applies the retention policy and reclaims the space. Omitting `keep` uses the
 * configured retention; passing 0 drops every checkpoint.
 */
export async function pruneVersioningCheckpoints(
  rootId: string,
  keep?: number,
): Promise<VersioningPrunePayload> {
  return protectedJSON<VersioningPrunePayload>(
    appURL("/api/versioning/checkpoints/prune"),
    versioningBody(rootId, keep === undefined ? {} : { keep }),
  );
}
