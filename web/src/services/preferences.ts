import { protectedJSON } from "./api";
import { appPath } from "./base";

function pinnedModels(value: unknown): string[] {
  const models = (value as { models?: unknown } | null)?.models;
  return Array.isArray(models) ? models.filter((id): id is string => typeof id === "string") : [];
}

let modelPinsRevision = 0;
const modelPinListeners = new Set<() => void>();
const modelPinCache = new Map<string, { models: string[]; revision: number }>();
const modelPinKey = (scope: string, agent: string) => `${scope}\u0000${agent}`;
export const getModelPinsRevision = () => modelPinsRevision;
export function subscribeModelPins(listener: () => void): () => void {
  modelPinListeners.add(listener);
  return () => { modelPinListeners.delete(listener); };
}
export function getModelPins(agent: string, fallback: string[] = []): string[] {
  return modelPinCache.get(modelPinKey(appPath("/"), agent))?.models ?? fallback;
}

// Hydrate from the existing Agent list. A response that started before a pin
// was saved must not overwrite that newer local result.
export function hydrateModelPins(agents: { name: string; pinned_models?: string[] }[], scope: string, requestRevision: number): void {
  const revision = ++modelPinsRevision;
  for (const agent of agents) {
    const key = modelPinKey(scope, agent.name);
    if ((modelPinCache.get(key)?.revision ?? 0) > requestRevision) continue;
    modelPinCache.set(key, { models: agent.pinned_models ?? [], revision });
  }
  modelPinListeners.forEach((listener) => listener());
}

export async function setModelPinned(agent: string, model: string, pinned: boolean): Promise<string[]> {
  const scope = appPath("/");
  const models = pinnedModels(await protectedJSON(appPath("/api/preferences/model-pins"), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, model, pinned }),
  }));
  modelPinCache.set(modelPinKey(scope, agent), { models, revision: ++modelPinsRevision });
  modelPinListeners.forEach((listener) => listener());
  return models;
}

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
