import type { ReactNode } from "react";

import "./Card.css";

import { classes } from "./classes";

// One file per component (.claude/rules/design-system.md), reproducing the system's `card` class
// rather than inventing a parallel one. The styling lives in the co-located Card.css and reads only
// from the token layer.
//
// `elevation` is a prop rather than a caller-supplied className because elevation is a closed set of
// three named steps in this system, and "never stack them" is only enforceable if the component owns
// the choice. Cards carry no shadow by default: the card is a surface fill.
export type CardElevation = "sm" | "md" | "lg";

// A lookup, not `elev-${elevation}` — see the note in Button.tsx.
const ELEVATION_CLASS: Record<CardElevation, string> = {
  sm: "elev-sm",
  md: "elev-md",
  lg: "elev-lg",
};

export function Card({
  children,
  elevation,
}: {
  children: ReactNode;
  elevation?: CardElevation;
}) {
  return (
    <section className={classes("card", elevation !== undefined && ELEVATION_CLASS[elevation])}>
      {children}
    </section>
  );
}
