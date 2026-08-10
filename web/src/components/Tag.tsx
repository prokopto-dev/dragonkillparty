import type { ReactNode } from "react";

import "./Tag.css";

import { classes } from "./classes";

// The system's `tag` class and its variants. `accent-2` is present because the source sheet defines
// it, and it reads identically to `accent`: Nocturne is a mono scheme.
export type TagVariant = "accent" | "accent-2" | "neutral" | "outline";

// A lookup, not `tag-${variant}` — see the note in Button.tsx.
const VARIANT_CLASS: Record<TagVariant, string> = {
  accent: "tag-accent",
  "accent-2": "tag-accent-2",
  neutral: "tag-neutral",
  outline: "tag-outline",
};

export function Tag({ children, variant }: { children: ReactNode; variant?: TagVariant }) {
  return (
    <span className={classes("tag", variant !== undefined && VARIANT_CLASS[variant])}>
      {children}
    </span>
  );
}
