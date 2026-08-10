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

// The header row, which occupies aria-rowindex 1 and therefore counts toward aria-rowcount.
const HEADER_ROWS = 1;

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
    // getItemKey is what makes `rowKey` mean anything to the VIRTUALIZER, as opposed to React.
    //
    // Without it the virtualizer defaults to the index, so its measurement cache is keyed by POSITION.
    // This component exists for server-side sort and filter, which is exactly the operation that
    // re-orders `rows` under stable identities: every measured height would stay attached to the old
    // index, `getTotalSize()` and the spacer offsets would be computed from heights belonging to other
    // rows, and the viewport would jump as rows were remeasured — worst for offscreen rows, which are
    // never remeasured at all. Passing it to the React `<tr key>` alone does not touch that cache; they
    // are two separate identity maps and both need the same key. Found in review.
    getItemKey: (index) => rowKey(rows[index]),
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
      {/*
        aria-rowcount and aria-rowindex share one 1-based space in which the header is row 1, so the
        count is the data rows PLUS the header. Passing rows.length made the last of 200 rows announce
        "row 201 of 200".
      */}
      <Table rowCount={rows.length + HEADER_ROWS} label={label}>
        <thead>
          <tr aria-rowindex={1}>
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
                // 1-based, past the header, so a virtualised row reports its true position in the
                // collection rather than its position in the DOM.
                aria-rowindex={item.index + HEADER_ROWS + 1}
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
