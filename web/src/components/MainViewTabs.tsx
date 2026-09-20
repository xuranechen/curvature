import { useI18n } from "../i18n";
import type { MainContentViewMode } from "./DefaultListView";

// The four main-area views. This is deliberately a component rather than inline
// JSX in App because it renders in two places:
//
//   - inline, as the leading part of DefaultListView's own header row, so the
//     directory listing has a single toolbar instead of two stacked bars;
//   - as a standalone bar, whenever the directory listing is not what the main
//     area is showing (a file, a git diff, a plugin view, or a session).
//
// Only one of the two is ever mounted, so the data-onboarding hook stays unique.
// The standalone case is what keeps the tabs reachable from every surface.

type MainViewTabsProps = {
  value: MainContentViewMode;
  onChange: (mode: MainContentViewMode) => void;
  variant?: "bar" | "inline";
};

export function MainViewTabs({ value, onChange, variant = "bar" }: MainViewTabsProps) {
  const { t } = useI18n();

  const tabs = [
    ["task-kanban", t("directory.taskKanban")],
    ["file-browser", t("directory.fileBrowser")],
    ["time-machine", t("directory.timeMachine")],
    ["session", t("mainViewTabs.session")],
  ] as const;

  return (
    <div
      role="tablist"
      data-onboarding="main-view-tabs"
      aria-label={t("mainViewTabs.ariaLabel")}
      style={
        variant === "bar"
          ? {
              display: "flex",
              alignItems: "center",
              gap: "2px",
              padding: "0 8px",
              minHeight: "32px",
              flexShrink: 0,
              borderBottom: "1px solid var(--border-color)",
              background: "var(--curvature-topbar-bg, transparent)",
              overflowX: "auto",
              boxSizing: "border-box",
            }
          : {
              display: "flex",
              alignItems: "center",
              gap: "2px",
              flexShrink: 0,
              minWidth: 0,
            }
      }
    >
      {tabs.map(([mode, label]) => {
        const active = value === mode;
        return (
          <button
            key={mode}
            type="button"
            role="tab"
            aria-selected={active}
            title={label}
            onClick={() => onChange(mode)}
            style={{
              border: "none",
              borderRadius: "6px",
              background: active ? "var(--accent-color)" : "transparent",
              color: active ? "#fff" : "var(--text-secondary)",
              fontSize: "12px",
              fontWeight: active ? 700 : 500,
              lineHeight: "16px",
              padding: "4px 9px",
              cursor: "pointer",
              whiteSpace: "nowrap",
              boxShadow: active ? "0 1px 3px rgba(37, 99, 235, 0.28)" : "none",
            }}
          >
            {label}
          </button>
        );
      })}
    </div>
  );
}
