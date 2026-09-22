import React, { useCallback, useEffect, useRef, useState } from "react";
import { useI18n } from "../i18n";

/** One navigable turn: a human prompt plus the reply that settled after it. */
export type TurnRailItem = {
  id: string;
  /** 1-based position, matching `data-user-message-index`. */
  index: number;
  prompt: string;
  response: string;
};

type SessionTurnRailProps = {
  /** The conversation area the rail reserves its column from. */
  areaRef: React.RefObject<HTMLDivElement | null>;
  /** The transcript scroller, so a wheel over the rail still scrolls the chat. */
  scrollRef: React.RefObject<HTMLDivElement | null>;
  items: TurnRailItem[];
  /** 1-based index of the turn currently in view; 0 when unknown. */
  activeIndex: number;
  /** 1-based index of the in-flight turn, 0 for none. */
  busyIndex?: number;
  /** Pixels the composer covers at the bottom of the area. */
  composerInset?: number;
  onSelect: (index: number) => void;
};

const RAIL_WIDTH = 36;
/** Preferred distance between two bars, before the rail starts compressing. */
const PREFERRED_GAP = 28;
/** Height of one bar's hover/click target. */
const ROW_HEIGHT = 24;
const RAIL_PADDING_Y = 16;
const BAR_WIDTH = 12;
const BAR_ACTIVE_WIDTH = 20;
const BAR_HOVER_WIDTH = 18;
const BAR_HEIGHT = 2;
const BAR_INSET_RIGHT = 10;
const MIN_AREA_WIDTH = 380;
const PREVIEW_MAX_HEIGHT = 100;
const PREVIEW_MIN_WIDTH = 160;
const PREVIEW_MAX_WIDTH = 300;
const PREVIEW_GAP = 10;

type RailMetrics = {
  visible: boolean;
  height: number;
  previewWidth: number;
};

const EMPTY_METRICS: RailMetrics = {
  visible: false,
  height: 0,
  previewWidth: PREVIEW_MAX_WIDTH,
};

/**
 * The conversation's turn rail: its own column to the right of the chat, one bar
 * per turn, spread over the column, with a hover preview and click-to-jump.
 */
export function SessionTurnRail({
  areaRef,
  scrollRef,
  items,
  activeIndex,
  busyIndex = 0,
  composerInset = 0,
  onSelect,
}: SessionTurnRailProps) {
  const { t } = useI18n();
  const [metrics, setMetrics] = useState<RailMetrics>(EMPTY_METRICS);
  const [hoveredIndex, setHoveredIndex] = useState(0);

  const itemsCountRef = useRef(items.length);
  itemsCountRef.current = items.length;

  const measure = useCallback(() => {
    const area = areaRef.current;
    if (!area) {
      return;
    }
    const width = area.clientWidth;
    const height = area.clientHeight;
    setMetrics((prev) => {
      const next: RailMetrics = {
        visible: itemsCountRef.current > 0 && width >= MIN_AREA_WIDTH,
        height,
        previewWidth: Math.max(
          PREVIEW_MIN_WIDTH,
          Math.min(PREVIEW_MAX_WIDTH, width - 120),
        ),
      };
      if (
        prev.visible === next.visible &&
        prev.height === next.height &&
        prev.previewWidth === next.previewWidth
      ) {
        return prev;
      }
      return next;
    });
  }, [areaRef]);

  useEffect(() => {
    const area = areaRef.current;
    if (!area || typeof window === "undefined") {
      return;
    }
    measure();
    if (typeof ResizeObserver === "undefined") {
      window.addEventListener("resize", measure);
      return () => window.removeEventListener("resize", measure);
    }
    const observer = new ResizeObserver(() => measure());
    observer.observe(area);
    window.addEventListener("resize", measure);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [areaRef, measure]);

  useEffect(() => {
    if (items.length === 0) {
      setHoveredIndex(0);
    }
    measure();
  }, [items.length, measure]);

  const usableTop = RAIL_PADDING_Y;
  const usableBottom = Math.max(
    usableTop,
    metrics.height - RAIL_PADDING_Y - Math.max(0, composerInset),
  );
  const usableHeight = Math.max(0, usableBottom - usableTop);
  const usableCenter = (usableTop + usableBottom) / 2;
  const count = items.length;
  const span =
    count > 1 ? Math.min(usableHeight, (count - 1) * PREFERRED_GAP) : 0;
  const step = count > 1 ? span / (count - 1) : 0;
  const firstCenter = metrics.height > 0 ? usableCenter - span / 2 : 0;
  const rowCenter = (position: number) =>
    count <= 1 ? usableCenter : firstCenter + step * position;

  if (!metrics.visible || metrics.height <= 0) {
    return null;
  }

  const hoveredPosition = items.findIndex((item) => item.index === hoveredIndex);
  const hoveredItem = hoveredPosition >= 0 ? items[hoveredPosition] : undefined;
  const previewTop =
    hoveredPosition < 0
      ? 0
      : Math.max(
          0,
          Math.min(
            Math.max(0, metrics.height - PREVIEW_MAX_HEIGHT),
            rowCenter(hoveredPosition) - PREVIEW_MAX_HEIGHT / 2,
          ),
        );

  return (
    <div
      data-curvature-turn-rail="1"
      role="navigation"
      aria-label={t("session.userSummary")}
      onWheel={(event) => {
        const container = scrollRef.current;
        if (container) {
          container.scrollTop += event.deltaY;
        }
      }}
      style={{
        position: "relative",
        flex: `0 0 ${RAIL_WIDTH}px`,
        width: `${RAIL_WIDTH}px`,
        alignSelf: "stretch",
        zIndex: 5,
      }}
    >
      {hoveredItem ? (
        <div
          role="tooltip"
          style={{
            position: "absolute",
            right: `calc(100% + ${PREVIEW_GAP}px)`,
            top: `${previewTop}px`,
            zIndex: 6,
            boxSizing: "border-box",
            width: `${metrics.previewWidth}px`,
            maxHeight: `${PREVIEW_MAX_HEIGHT}px`,
            padding: "10px 12px",
            borderRadius: "10px",
            border: "1px solid var(--menu-border)",
            background: "var(--menu-bg)",
            boxShadow: "0 16px 34px rgba(15, 23, 42, 0.18)",
            color: "var(--text-primary)",
            pointerEvents: "none",
            overflow: "hidden",
          }}
        >
          <span
            style={{
              display: "-webkit-box",
              WebkitBoxOrient: "vertical",
              WebkitLineClamp: 1,
              overflow: "hidden",
              fontSize: "13px",
              fontWeight: 600,
              lineHeight: "18px",
            }}
          >
            {hoveredItem.prompt}
          </span>
          {hoveredItem.response ? (
            <span
              style={{
                display: "-webkit-box",
                WebkitBoxOrient: "vertical",
                WebkitLineClamp: 3,
                overflow: "hidden",
                marginTop: "4px",
                fontSize: "12px",
                lineHeight: "17px",
                color: "var(--text-secondary)",
              }}
            >
              {hoveredItem.response}
            </span>
          ) : null}
        </div>
      ) : null}
      {items.map((item, position) => {
        const isActive = item.index === activeIndex;
        const isHovered = item.index === hoveredIndex;
        const isBusy = busyIndex > 0 && item.index === busyIndex;
        const barWidth = isActive
          ? BAR_ACTIVE_WIDTH
          : isHovered
            ? BAR_HOVER_WIDTH
            : BAR_WIDTH;
        const barColor = isActive
          ? "var(--text-primary)"
          : isHovered
            ? "var(--text-secondary)"
            : "color-mix(in srgb, var(--text-secondary) 38%, transparent)";
        return (
          <button
            key={item.id}
            type="button"
            aria-label={item.prompt}
            aria-current={isActive ? "true" : undefined}
            title={item.prompt}
            onMouseEnter={() => setHoveredIndex(item.index)}
            onMouseLeave={() =>
              setHoveredIndex((prev) => (prev === item.index ? 0 : prev))
            }
            onFocus={() => setHoveredIndex(item.index)}
            onBlur={() =>
              setHoveredIndex((prev) => (prev === item.index ? 0 : prev))
            }
            onClick={() => onSelect(item.index)}
            style={{
              position: "absolute",
              left: 0,
              right: 0,
              top: `${rowCenter(position)}px`,
              height: `${ROW_HEIGHT}px`,
              transform: "translateY(-50%)",
              display: "flex",
              alignItems: "center",
              justifyContent: "flex-end",
              padding: 0,
              paddingRight: `${BAR_INSET_RIGHT}px`,
              border: "none",
              background: "transparent",
              cursor: "pointer",
              transition: "top 0.18s ease",
            }}
          >
            <span
              aria-hidden="true"
              style={{
                width: `${barWidth}px`,
                height: `${BAR_HEIGHT}px`,
                borderRadius: "2px",
                background: barColor,
                transition: "width 0.14s ease, background-color 0.14s ease",
                animation: isBusy
                  ? "curvatureTurnRailBusy 1s ease-in-out infinite"
                  : undefined,
              }}
            />
          </button>
        );
      })}
      <style>{`
        @keyframes curvatureTurnRailBusy {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.35; }
        }
      `}</style>
    </div>
  );
}
