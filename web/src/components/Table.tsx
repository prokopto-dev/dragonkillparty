import type { ReactNode } from "react";

import "./Table.css";

// The system's `table` class on a real <table>. The class does the whole job, so this component
// exists to own the CSS import and to keep every screen using the same element — not to abstract the
// table away. Screens pass <thead>/<tbody> as children and keep their own markup.
//
// `rowCount` sets aria-rowcount, which is how a VIRTUALISED table tells assistive technology how
// many rows exist rather than how many are currently in the DOM. Ordinary tables omit it.
export function Table({
  children,
  rowCount,
  label,
}: {
  children: ReactNode;
  rowCount?: number;
  label?: string;
}) {
  return (
    <table className="table" aria-rowcount={rowCount} aria-label={label}>
      {children}
    </table>
  );
}
