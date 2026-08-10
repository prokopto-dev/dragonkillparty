import type { ReactNode } from "react";

import "./Table.css";

// The system's `table` class on a real <table>. The class does the whole job, so this component
// exists to own the CSS import and to keep every screen using the same element — not to abstract the
// table away. Screens pass <thead>/<tbody> as children and keep their own markup.
//
// `rowCount` sets aria-rowcount, which is how a VIRTUALISED table tells assistive technology how
// many rows exist rather than how many are currently in the DOM. Ordinary tables omit it.
//
// It is the TOTAL row count INCLUDING header rows, because aria-rowindex is 1-based over the same
// space and the header occupies index 1. Passing the data-row count instead makes the last row report
// index N+1 against a count of N — a table that announces "row 201 of 200". The name says so and
// VirtualTable computes it explicitly.
export function Table({
  children,
  rowCount,
  label,
}: {
  children: ReactNode;
  /** Total rows including the header row, for aria-rowcount. */
  rowCount?: number;
  label?: string;
}) {
  return (
    <table className="table" aria-rowcount={rowCount} aria-label={label}>
      {children}
    </table>
  );
}
