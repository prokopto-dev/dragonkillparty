import { useRef, type ReactNode } from "react";

import { useVirtualizer } from "@tanstack/react-virtual";

import "./VirtualTable.css";

import { Table } from "./Table";

// A row-virtualised .table.
//
// Standings for 200 characters x 12 columns is the heaviest view in the product and the reason the
// bundle budget exists (.claude/rules/web.md): a volunteer's Raspberry Pi is the deployment target.
// Sort and filter are SERVER-SIDE — never fetch the whole collection and sort in the browser — so
// this component virtualises a page of rows the server already ordered, and knows nothing about
// ordering itself.
//
// It keeps a real <table>/<thead>/<tbody> with spacer rows rather than absolutely-positioned divs,
// because .table's row-level fading hairline is painted on <tr> and cannot survive being turned into
// a stack of positioned boxes. Semantics and the look come out of the same decision.

/** One column. `cell` renders the value; the row type stays the caller's. */
export type VirtualTableColumn<Row> = {
  key: string;
  header: ReactNode;
  cell: (row: Row) => ReactNode;
};

// First-frame scrollbar estimate only: `measureElement` replaces it with each row's real height as
// the rows mount, so a row that wraps is not mis-sized. It is not a design token — nothing in CSS
// fixes a row height, and adding one would break a wrapping cell.
const ROW_HEIGHT_ESTIMATE = 32;

export function VirtualTable<Row>({
  columns,
  rows,
  rowKey,
  height,
  label,
}: {
  columns: VirtualTableColumn<Row>[];
  rows: Row[];
  rowKey: (row: Row) => string;
  /** Viewport height in CSS pixels. A layout measurement, not a design value. */
  height: number;
  label: string;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT_ESTIMATE,
    overscan: 8,
  });

  const items = virtualizer.getVirtualItems();
  const first = items.at(0);
  const last = items.at(-1);
  const padBefore = first === undefined ? 0 : first.start;
  const padAfter = last === undefined ? 0 : virtualizer.getTotalSize() - last.end;

  return (
    // tabIndex makes the scroll region reachable by keyboard: a scrollable box that cannot be focused
    // cannot be scrolled without a pointer.
    <div ref={scrollRef} className="virtual-table" style={{ height }} tabIndex={0} role="group">
      <Table rowCount={rows.length} label={label}>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key} scope="col">
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {padBefore > 0 && <SpacerRow span={columns.length} height={padBefore} />}
          {items.map((item) => {
            const row = rows[item.index];

            return (
              <tr
                key={rowKey(row)}
                // aria-rowindex is 1-based and counts the header row, so a virtualised row still
                // reports its true position in the collection rather than its position in the DOM.
                aria-rowindex={item.index + 2}
                data-index={item.index}
                ref={virtualizer.measureElement}
              >
                {columns.map((column) => (
                  <td key={column.key}>{column.cell(row)}</td>
                ))}
              </tr>
            );
          })}
          {padAfter > 0 && <SpacerRow span={columns.length} height={padAfter} />}
        </tbody>
      </Table>
    </div>
  );
}

// The spacers hold the scroll height the unrendered rows would occupy. aria-hidden because they are
// layout, not data: without it a screen reader announces two empty rows in every table.
function SpacerRow({ span, height }: { span: number; height: number }) {
  return (
    <tr className="virtual-table-spacer" aria-hidden="true">
      <td colSpan={span} style={{ height }} />
    </tr>
  );
}
