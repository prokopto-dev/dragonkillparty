import type { ReactNode } from "react";

import "./Card.css";

// One file per component (.claude/rules/design-system.md), reproducing the system's `card` class
// rather than inventing a parallel one. Elevation is a hairline ring plus ambient darkness — the
// styling lives in the co-located Card.css and reads only from the token layer.
export function Card({ children }: { children: ReactNode }) {
  return <section className="card">{children}</section>;
}
