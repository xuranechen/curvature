import { protectedJSON } from "./api";
import { appPath } from "./base";

export type SessionNamingPreference = {
  agent: string;
  model: string;
  disabled: boolean;
};

export type IdleSessionResourceReleasePreference = {
  hours: number;
};

export type NewProjectMetaLocation = "project" | "home";

function normalizeNewProjectMetaLocation(value: unknown): NewProjectMetaLocation {
  const location = value && typeof value === "object" && "location" in value
    ? String((value as { location?: unknown }).location || "")
    : "";
  return location === "home" ? "home" : "project";
}

export async function fetchNewProjectMetaLocationPreference(): Promise<NewProjectMetaLocation> {
  return normalizeNewProjectMetaLocation(
    await protectedJSON(appPath("/api/preferences/new-project-meta-location")),
  );
}

export async function updateNewProjectMetaLocationPreference(
  location: NewProjectMetaLocation,
): Promise<NewProjectMetaLocation> {
  return normalizeNewProjectMetaLocation(
    await protectedJSON(appPath("/api/preferences/new-project-meta-location"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ location }),
    }),
  );
}

const DEFAULT_IDLE_SESSION_RESOURCE_RELEASE_HOURS = 72;

function normalizeIdleSessionResourceReleasePreference(
  value: unknown,
): IdleSessionResourceReleasePreference {
  const input = value && typeof value === "object"
    ? value as Partial<IdleSessionResourceReleasePreference>
    : {};
  const hours = Number(input.hours);
  return {
    hours: Number.isInteger(hours) && hours > 0
      ? hours
      : DEFAULT_IDLE_SESSION_RESOURCE_RELEASE_HOURS,
  };
}

function normalizeSessionNamingPreference(value: unknown): SessionNamingPreference {
  const input = value && typeof value === "object"
    ? value as Partial<SessionNamingPreference>
    : {};
  return {
    agent: typeof input.agent === "string" ? input.agent.trim() : "",
    model: typeof input.model === "string" ? input.model.trim() : "",
    disabled: input.disabled === true,
  };
}

export async function fetchSessionNamingPreference(): Promise<SessionNamingPreference> {
  return normalizeSessionNamingPreference(
    await protectedJSON(appPath("/api/preferences/session-naming")),
  );
}

export async function updateSessionNamingPreference(
  preference: SessionNamingPreference,
): Promise<SessionNamingPreference> {
  return normalizeSessionNamingPreference(
    await protectedJSON(appPath("/api/preferences/session-naming"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(preference),
    }),
  );
}

export async function fetchIdleSessionResourceReleasePreference(): Promise<IdleSessionResourceReleasePreference> {
  return normalizeIdleSessionResourceReleasePreference(
    await protectedJSON(appPath("/api/preferences/idle-session-resource-release")),
  );
}

export async function updateIdleSessionResourceReleasePreference(
  preference: IdleSessionResourceReleasePreference,
): Promise<IdleSessionResourceReleasePreference> {
  return normalizeIdleSessionResourceReleasePreference(
    await protectedJSON(appPath("/api/preferences/idle-session-resource-release"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(preference),
    }),
  );
}

export type VersioningAutoCheckpointsPreference = {
  enabled: boolean;
};

// Automatic checkpoints default to on, so only an explicit `false` from the
// server means disabled. This mirrors the backend, which stores the preference
// as a negated flag so an unset value reads as enabled.
function normalizeVersioningAutoCheckpointsPreference(
  value: unknown,
): VersioningAutoCheckpointsPreference {
  const input = value && typeof value === "object"
    ? value as Partial<VersioningAutoCheckpointsPreference>
    : {};
  return { enabled: input.enabled !== false };
}

export async function fetchVersioningAutoCheckpointsPreference(): Promise<VersioningAutoCheckpointsPreference> {
  return normalizeVersioningAutoCheckpointsPreference(
    await protectedJSON(appPath("/api/preferences/versioning-auto-checkpoints")),
  );
}

export async function updateVersioningAutoCheckpointsPreference(
  enabled: boolean,
): Promise<VersioningAutoCheckpointsPreference> {
  return normalizeVersioningAutoCheckpointsPreference(
    await protectedJSON(appPath("/api/preferences/versioning-auto-checkpoints"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled }),
    }),
  );
}

// Agent autonomy is the opposite convention from the switch above: it defaults
// to off, so an unset value must read as denied. Normalizing with `=== true` is
// what enforces that; copying the `!== false` form here would silently hand
// agents the right to rewrite the working tree.
export type VersioningAgentAutonomyPreference = {
  enabled: boolean;
};

function normalizeVersioningAgentAutonomyPreference(
  value: unknown,
): VersioningAgentAutonomyPreference {
  const input = value && typeof value === "object"
    ? value as Partial<VersioningAgentAutonomyPreference>
    : {};
  return { enabled: input.enabled === true };
}

export async function fetchVersioningAgentAutonomyPreference(): Promise<VersioningAgentAutonomyPreference> {
  return normalizeVersioningAgentAutonomyPreference(
    await protectedJSON(appPath("/api/preferences/versioning-agent-autonomy")),
  );
}

export async function updateVersioningAgentAutonomyPreference(
  enabled: boolean,
): Promise<VersioningAgentAutonomyPreference> {
  return normalizeVersioningAgentAutonomyPreference(
    await protectedJSON(appPath("/api/preferences/versioning-agent-autonomy"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled }),
    }),
  );
}

// Retention bounds the store, so the backend rejects a zero or negative count
// and resolves an unset one to its own default. Anything the client cannot read
// as a positive count therefore falls back to that same default rather than
// disabling retention.
export const DEFAULT_VERSIONING_CHECKPOINT_RETENTION = 200;

export type VersioningCheckpointRetentionPreference = {
  keep: number;
};

function normalizeVersioningCheckpointRetentionPreference(
  value: unknown,
): VersioningCheckpointRetentionPreference {
  const input = value && typeof value === "object"
    ? value as { keep?: unknown }
    : {};
  const keep = typeof input.keep === "number" && Number.isFinite(input.keep) && input.keep > 0
    ? Math.floor(input.keep)
    : DEFAULT_VERSIONING_CHECKPOINT_RETENTION;
  return { keep };
}

export async function fetchVersioningCheckpointRetentionPreference(): Promise<VersioningCheckpointRetentionPreference> {
  return normalizeVersioningCheckpointRetentionPreference(
    await protectedJSON(appPath("/api/preferences/versioning-checkpoint-retention")),
  );
}

export async function updateVersioningCheckpointRetentionPreference(
  keep: number,
): Promise<VersioningCheckpointRetentionPreference> {
  return normalizeVersioningCheckpointRetentionPreference(
    await protectedJSON(appPath("/api/preferences/versioning-checkpoint-retention"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep }),
    }),
  );
}
