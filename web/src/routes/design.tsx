import { useState, type ReactNode } from "react";

import { createRoute } from "@tanstack/react-router";

import { Button } from "@/components/Button";
import { Card } from "@/components/Card";
import { Dialog } from "@/components/Dialog";
import { Field, Input, TextArea } from "@/components/Field";
import { Radio } from "@/components/Radio";
import { Seg, SegOption } from "@/components/Seg";
import { Table } from "@/components/Table";
import { Tag } from "@/components/Tag";
import { VirtualTable, type VirtualTableColumn } from "@/components/VirtualTable";

import { rootRoute } from "./root";
import "./design.css";

/*
 * /_design — the Nocturne foundation, rendered.
 *
 * Every custom property in web/src/styles/tokens.css appears here as a swatch, a bar or a type
 * sample, and every base component class appears here as the real class on the real markup. That is
 * the point: the vocabulary docs/design/09-frontend-and-design-system.md describes is either visible
 * on this page or it does not exist, and Phase 3 is written against this vocabulary.
 *
 * test/repo/design_tokens_test.go holds the page to that promise: a token added to tokens.css
 * without a swatch here fails, and so does a component module that this page does not import. The
 * page cannot quietly fall behind the sheet.
 *
 * IT SHIPS IN THE PRODUCTION BUNDLE, ON PURPOSE. This route is what makes `make budget-bundle`
 * measure the shell verify-before-phase-0 V13 specifies — React + Router + Query + Virtual plus one
 * 12-column virtualised table — on every PR, instead of a shell that has quietly shrunk below the
 * number the budget was derived from. Lazy-loading it later means re-deriving the budget in the same
 * change. See web/bundle-budget.json.
 *
 * Strings here are fixture labels — token names and component names — not product copy, which is why
 * they are not candidates for the Phase 3 message catalogue.
 */

// ─── The token vocabulary ────────────────────────────────────────────────────────────────────────
//
// Written out one token at a time rather than generated from a step list. The names must be greppable
// as literals: test/repo/design_tokens_test.go compares the set of names in this file against the set
// declared in tokens.css, and a generated `--color-${role}-${step}` would defeat a check that is
// deliberately dumb.

type Swatches = { title: string; note?: string; tokens: string[] };

const COLOR_GROUPS: Swatches[] = [
  {
    title: "Roles",
    note: "--color-accent is not --color-accent-500. Both exist; do not collapse them. --color-accent-2 is a mono scheme's stand-in for the same role, not a second voice.",
    tokens: [
      "--color-bg",
      "--color-surface",
      "--color-text",
      "--color-accent",
      "--color-accent-2",
      "--color-divider",
    ],
  },
  {
    title: "Neutral ramp",
    note: "Step N of any ramp carries the same visual weight as step N of any other. 700–900 tinted fills, 500 the role base, 100–300 text on those tints.",
    tokens: [
      "--color-neutral-100",
      "--color-neutral-200",
      "--color-neutral-300",
      "--color-neutral-400",
      "--color-neutral-500",
      "--color-neutral-600",
      "--color-neutral-700",
      "--color-neutral-800",
      "--color-neutral-900",
    ],
  },
  {
    title: "Accent ramp",
    tokens: [
      "--color-accent-100",
      "--color-accent-200",
      "--color-accent-300",
      "--color-accent-400",
      "--color-accent-500",
      "--color-accent-600",
      "--color-accent-700",
      "--color-accent-800",
      "--color-accent-900",
    ],
  },
  {
    title: "Accent-2 ramp",
    tokens: [
      "--color-accent-2-100",
      "--color-accent-2-200",
      "--color-accent-2-300",
      "--color-accent-2-400",
      "--color-accent-2-500",
      "--color-accent-2-600",
      "--color-accent-2-700",
      "--color-accent-2-800",
      "--color-accent-2-900",
    ],
  },
  {
    title: "Section",
    note: "Deck scale only. Sanctioned in exactly two places: the public site's full-bleed stat band, and the site-wide quake banner. Nowhere else.",
    tokens: ["--color-section", "--color-section-glow", "--color-section-ghost"],
  },
  {
    title: "Success",
    note: "Never use hue alone to carry meaning — every status also carries an icon and a word. -800/-900 ground, -300 text on it, -400 icon, -700 border.",
    tokens: [
      "--color-success-300",
      "--color-success-400",
      "--color-success-700",
      "--color-success-800",
      "--color-success-900",
    ],
  },
  {
    title: "Warning",
    tokens: [
      "--color-warning-300",
      "--color-warning-400",
      "--color-warning-700",
      "--color-warning-800",
      "--color-warning-900",
    ],
  },
  {
    title: "Danger",
    tokens: [
      "--color-danger-300",
      "--color-danger-400",
      "--color-danger-700",
      "--color-danger-800",
      "--color-danger-900",
    ],
  },
  {
    title: "soft(p) — muted text, hairlines, fills",
    note: "color-mix of --color-text with transparent. The checkerboard behind each chip is what makes the translucency visible.",
    tokens: [
      "--soft-4",
      "--soft-7",
      "--soft-8",
      "--soft-14",
      "--soft-45",
      "--soft-50",
      "--soft-55",
      "--soft-60",
      "--soft-70",
    ],
  },
  {
    title: "tint(p) — selected, active, highlight",
    note: "color-mix of --color-accent with transparent. The accent is a line and a glow, never a flood.",
    tokens: ["--tint-10", "--tint-12", "--tint-18", "--tint-22", "--tint-30"],
  },
  {
    title: "Scrim",
    note: "The dialog backdrop: a --color-neutral-900 mix, so neither a soft() nor a tint() rung.",
    tokens: ["--scrim"],
  },
];

const SPACE_TOKENS = [
  "--space-1",
  "--space-2",
  "--space-3",
  "--space-4",
  "--space-6",
  "--space-8",
  "--space-10",
  "--space-12",
  "--space-16",
  "--space-20",
  "--space-24",
];

const FONT_SIZE_TOKENS = [
  "--font-size-3xs",
  "--font-size-2xs",
  "--font-size-xs",
  "--font-size-sm",
  "--font-size-md",
  "--font-size-base",
  "--font-size-lg",
  "--font-size-xl",
  "--font-size-2xl",
  "--font-size-3xl",
  "--font-size-4xl",
  "--font-size-5xl",
];

const RADIUS_TOKENS = ["--radius-xs", "--radius-sm", "--radius-md", "--radius-lg", "--radius-pill"];

const SHADOW_TOKENS = ["--shadow-sm", "--shadow-md", "--shadow-lg"];

const CONTROL_METRIC_TOKENS = [
  "--control-height",
  "--control-gap",
  "--control-gap-lg",
  "--control-pad-y",
  "--control-pad-x",
  "--seg-pad-y",
  "--seg-pad-x",
  "--tag-pad-y",
  "--field-label-gap",
  "--radio-dot-size",
  "--radio-dot-border",
  "--radio-dot-inset",
  "--textarea-min-height",
  "--dialog-width",
  "--hairline",
  "--hairline-fade",
  "--focus-ring",
  "--focus-offset",
  "--underline-offset",
];

// ─── Sample data for the virtualised table ───────────────────────────────────────────────────────
//
// Layout-only sample rows, generated from the index so the page is deterministic — no clock, no RNG,
// and no point arithmetic: the balance column here is an opaque display string, not Centipoints being
// divided in the browser. A real standings view reads *_centipoints from the generated client and
// formats with the guild's points_precision.

type SampleRow = {
  id: string;
  character: string;
  className: string;
  level: string;
  rank: string;
  attendance: string;
  earned: string;
  spent: string;
  balance: string;
  lastRaid: string;
  zone: string;
  status: string;
};

const SAMPLE_CLASSES = ["Cleric", "Warrior", "Enchanter", "Rogue", "Shaman", "Wizard"];
const SAMPLE_RANKS = ["Member", "Officer", "Recruit", "Alt"];

// FIVE zones against four ranks and six classes, and the count is deliberate: the cycles stay out of
// step, so no attribute of a row can be predicted from another. A four-entry list would put zone and
// rank in lockstep, and sorting by one would silently sort by the other.
const SAMPLE_ZONES = [
  "Plane of Hate",
  "Kael Drakkel",
  "Temple of Veeshan",
  "Sleeper's Tomb",
  "Plane of Sky",
];
const SAMPLE_STATUS = ["Active", "Active", "Inactive", "Active"];

const SAMPLE_ROWS: SampleRow[] = Array.from({ length: 200 }, (_, i) => ({
  id: `row-${String(i)}`,
  character: `Character ${String(i + 1).padStart(3, "0")}`,
  className: SAMPLE_CLASSES[i % SAMPLE_CLASSES.length],
  level: String(45 + (i % 16)),
  rank: SAMPLE_RANKS[i % SAMPLE_RANKS.length],
  attendance: `${String(40 + ((i * 7) % 60))}%`,
  earned: String(1200 + i * 13),
  spent: String(400 + i * 9),
  balance: String(800 + i * 4),
  lastRaid: `Day ${String((i % 28) + 1)}`,
  zone: SAMPLE_ZONES[i % SAMPLE_ZONES.length],
  status: SAMPLE_STATUS[i % SAMPLE_STATUS.length],
}));

// The same 200 rows, in the other order the server could have returned them.
//
// NOT A CLIENT-SIDE SORT. Sort and filter are server-side (.claude/rules/web.md) and VirtualTable
// knows nothing about ordering; this is a precomputed constant standing in for a second page of
// results, so the fixture can exercise the one operation `getItemKey` exists to survive — the same
// rows arriving under stable identities in a different order.
//
// BY RANK, and the choice is what gives e2e/virtual-table.spec.ts a signal it cannot miss. The tall
// rows are the alts (see SAMPLE_COLUMNS below), and "Alt" sorts first, so the top of the collection
// goes from one row in four being tall to EVERY row being tall. A re-order that merely reshuffled
// tall rows among themselves — reversing, say, where rank cycles every four rows — would leave any
// given window with the same number of tall rows in it, and the defect would cancel out of the
// measurement. Compared by code unit rather than localeCompare: a fixture's order must not depend on
// the machine's locale.
const SAMPLE_ROWS_BY_RANK: SampleRow[] = [...SAMPLE_ROWS].sort((a, b) => {
  if (a.rank === b.rank) {
    return 0;
  }

  return a.rank < b.rank ? -1 : 1;
});

const SAMPLE_COLUMNS: VirtualTableColumn<SampleRow>[] = [
  {
    key: "character",
    header: "Character",
    // AN ALT RENDERS ON TWO LINES, and that is the fixture's only source of nonuniform row height.
    // It is load-bearing: with every row the same height, a virtualizer that keys its measurement
    // cache by POSITION and one that keys it by ROW produce identical output, so the defect
    // VirtualTable's `getItemKey` comment describes cannot be observed at all — stale heights and
    // correct heights are the same number. e2e/virtual-table.spec.ts needs this; issue #33 says why.
    //
    // Height comes from MARKUP rather than from a long string that wraps, and the difference is not
    // stylistic. A <table> in auto layout sizes its columns from the cells currently rendered, so a
    // wrapping cell's height depends on which OTHER rows happen to be virtualised in beside it —
    // row height stops being a function of the row, and the invariant the test rests on ("the same
    // rows in a different order total the same height") becomes false for honest reasons. Measured:
    // a long zone name moved the total by ~90px across a re-order with a correct getItemKey. The
    // second line is also deliberately SHORTER than the character name above it, so it never becomes
    // the column's widest content and cannot shift the layout either.
    cell: (row) =>
      row.rank === "Alt" ? (
        <>
          {row.character}
          <div className="text-muted">alt</div>
        </>
      ) : (
        row.character
      ),
  },
  { key: "class", header: "Class", cell: (row) => row.className },
  { key: "level", header: "Level", cell: (row) => row.level },
  { key: "rank", header: "Rank", cell: (row) => row.rank },
  { key: "attendance", header: "Attendance", cell: (row) => row.attendance },
  { key: "earned", header: "Earned", cell: (row) => row.earned },
  { key: "spent", header: "Spent", cell: (row) => row.spent },
  { key: "balance", header: "Balance", cell: (row) => row.balance },
  { key: "last-raid", header: "Last raid", cell: (row) => row.lastRaid },
  { key: "zone", header: "Zone", cell: (row) => row.zone },
  { key: "alt-of", header: "Alt of", cell: (row) => (row.rank === "Alt" ? row.character : "—") },
  { key: "status", header: "Status", cell: (row) => row.status },
];

// The viewport height of the virtualised table, in CSS pixels. A layout measurement passed to the
// virtualizer, not a design value.
const VIRTUAL_TABLE_HEIGHT = 320;

// ─── The route ───────────────────────────────────────────────────────────────────────────────────

export const designRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/_design",
  component: DesignSystem,
});

function DesignSystem() {
  return (
    <main className="design">
      <header>
        <h1>Nocturne</h1>
        <p className="text-muted">
          Every token in web/src/styles/tokens.css and every base component class, rendered. The
          normative document is docs/design/09-frontend-and-design-system.md.
        </p>
      </header>

      <hr className="hr" />

      <Section title="Colour">
        {COLOR_GROUPS.map((group) => (
          <SwatchGroup key={group.title} group={group} />
        ))}
      </Section>

      <Section title="Type">
        <p className="text-muted">
          Density moves spacing, never sizes. Headings are 500 weight; hierarchy is size and space.
        </p>
        <p className="text-muted">
          These samples are NOT rendering in Inter. §3 requires the faces be self-hosted and none is
          vendored yet, so the stack falls through to system-ui and this section reads differently on
          every machine. Tracked in issue #45; until it lands, the type scale cannot be reviewed against
          the mockups here.
        </p>
        <div className="design-row">
          <span style={{ fontFamily: "var(--font-heading)", fontWeight: "var(--font-heading-weight)" }}>
            --font-heading at --font-heading-weight
          </span>
          <span style={{ fontFamily: "var(--font-body)" }}>--font-body at 400</span>
        </div>
        {FONT_SIZE_TOKENS.map((token) => (
          <div key={token} className="design-row">
            <span className="design-label">{token}</span>
            <span style={{ fontSize: `var(${token})` }}>Dragon Kill Party</span>
          </div>
        ))}
        <div>
          <h1>h1 Heading</h1>
          <h2>h2 Heading</h2>
          <h3>h3 Heading</h3>
          <h4>h4 Heading</h4>
          <h5>h5 Heading</h5>
          <h6>h6 Heading</h6>
          <p>
            Body copy at --font-size-base. A <a href="/_design">link</a> takes the accent, and{" "}
            <span className="text-muted">.text-muted</span> takes soft(55).
          </p>
        </div>
      </Section>

      <Section title="Space">
        <p className="text-muted">
          Base unit 4px x 0.70 — dense on purpose. There is no --space-5 or --space-7.
        </p>
        <Metrics tokens={SPACE_TOKENS} />
      </Section>

      <Section title="Radius">
        <div className="design-boxes">
          {RADIUS_TOKENS.map((token) => (
            <div key={token} className="design-swatch">
              <div className="design-box" style={{ borderRadius: `var(${token})` }} />
              <span className="design-label">{token}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Elevation">
        <p className="text-muted">
          A hairline ring plus ambient darkness, not a drop shadow. --shadow-sm has no blur at all —
          it is a ring. Never stack them.
        </p>
        <div className="design-boxes">
          {SHADOW_TOKENS.map((token) => (
            <div key={token} className="design-swatch">
              <div className="design-box" style={{ boxShadow: `var(${token})` }} />
              <span className="design-label">{token}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Control metrics">
        <p className="text-muted">
          Off the --space-* grid on purpose: Nocturne draws its controls on their own metric.
        </p>
        <Metrics tokens={CONTROL_METRIC_TOKENS} />
      </Section>

      <hr className="hr" />

      <Section title="Buttons">
        <p className="text-muted">
          Outlined, never filled. Primary is a 1px accent border on transparent, tint(12) hover,
          tint(22) active.
        </p>
        <div className="design-row">
          <Button variant="primary">Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="secondary" icon aria-label="Add">
            <PlusIcon />
          </Button>
          <Button variant="primary" disabled>
            Disabled
          </Button>
          <Button>Unstyled base</Button>
        </div>
        <div style={{ maxWidth: "var(--dialog-width)" }}>
          <Button variant="primary" block>
            Block
          </Button>
        </div>
      </Section>

      <Section title="Tags">
        <div className="design-row">
          <Tag variant="accent">accent</Tag>
          <Tag variant="accent-2">accent-2</Tag>
          <Tag variant="neutral">neutral</Tag>
          <Tag variant="outline">outline</Tag>
        </div>
      </Section>

      <Section title="Cards">
        <div className="design-cards">
          <Card>
            <span className="card-kicker">Kicker</span>
            <span className="card-title">Card title</span>
            <p className="card-body">
              The card is a surface fill and carries no shadow of its own. Elevation is a separate
              decision.
            </p>
            <div className="card-meta">card-meta</div>
          </Card>
          <Card elevation="sm">
            <span className="card-title">elev-sm</span>
            <p className="card-body">A hairline ring, no blur.</p>
          </Card>
          <Card elevation="md">
            <span className="card-title">elev-md</span>
            <p className="card-body">Ring plus ambient darkness.</p>
          </Card>
          <Card elevation="lg">
            <span className="card-title">elev-lg</span>
            <p className="card-body">The top elevation — dialogs.</p>
          </Card>
        </div>
      </Section>

      <Section title="Forms">
        <div className="design-forms">
          <Field label="Text input">
            <Input defaultValue="Grimwald Ashvane" />
          </Field>
          <Field label="Textarea">
            <TextArea defaultValue="Reversal reason" />
          </Field>
          <fieldset style={{ border: 0, margin: 0, padding: 0 }}>
            <legend className="design-label">radio + dot</legend>
            <div className="design-row">
              <Radio name="design-radio" defaultChecked>
                Quarantine
              </Radio>
              <Radio name="design-radio">Fail</Radio>
              <Radio name="design-radio">Create</Radio>
            </div>
          </fieldset>
          <div>
            <span className="design-label">seg + seg-opt</span>
            <div>
              <Seg label="Sample filter">
                <SegOption defaultChecked>
                  All
                </SegOption>
                <SegOption>Open</SegOption>
                <SegOption>Disputed</SegOption>
                <SegOption>Finalised</SegOption>
              </Seg>
            </div>
          </div>
        </div>
      </Section>

      <Section title="Table">
        <p className="text-muted">
          The row rules are row-level background gradients, fading to transparent over 48px at each
          end. A per-cell border cannot fade across a row.
        </p>
        <Table>
          <thead>
            <tr>
              <th scope="col">Character</th>
              <th scope="col">Class</th>
              <th scope="col">Balance</th>
            </tr>
          </thead>
          <tbody>
            {SAMPLE_ROWS.slice(0, 4).map((row) => (
              <tr key={row.id}>
                <td>{row.character}</td>
                <td>{row.className}</td>
                <td>{row.balance}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      </Section>

      <Section title="Virtualised table">
        <p className="text-muted">
          200 rows x 12 columns, the heaviest view in the product. Row virtualisation only — sort and
          filter are server-side. The order control swaps between two precomputed orders of the same
          rows; it stands in for the server returning them re-ordered, and nothing here sorts a
          collection in the browser.
        </p>
        <VirtualTableDemo />
      </Section>

      <Section title="Dialog">
        <DialogDemo />
      </Section>
    </main>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="design-section">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

function SwatchGroup({ group }: { group: Swatches }) {
  return (
    <div className="design-section">
      <h6>{group.title}</h6>
      {group.note !== undefined && <p className="text-muted">{group.note}</p>}
      <div className="design-swatches">
        {group.tokens.map((token) => (
          <div key={token} className="design-swatch">
            <div className="design-chip">
              <div className="design-chip-fill" style={{ background: `var(${token})` }} />
            </div>
            <span className="design-label">{token}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function Metrics({ tokens }: { tokens: string[] }) {
  return (
    <div className="design-metrics">
      {tokens.map((token) => (
        <div key={token} style={{ display: "contents" }}>
          <span className="design-label">{token}</span>
          <div className="design-bar" style={{ width: `var(${token})` }} />
        </div>
      ))}
    </div>
  );
}

// The virtualised table plus the control that re-orders it.
//
// The Seg here is doing a second job beyond appearing in the vocabulary: a re-order is the operation
// VirtualTable's `getItemKey` exists for, and until this control existed nothing in the repository
// could perform one. Keeping the two adjacent is the point — the segment IS how the fixture asks the
// table for a different order.
function VirtualTableDemo() {
  const [byRank, setByRank] = useState(false);

  return (
    <>
      <div className="design-row">
        <Seg label="Server order">
          <SegOption
            defaultChecked
            onChange={() => {
              setByRank(false);
            }}
          >
            By balance
          </SegOption>
          <SegOption
            onChange={() => {
              setByRank(true);
            }}
          >
            By rank
          </SegOption>
        </Seg>
      </div>
      <VirtualTable
        columns={SAMPLE_COLUMNS}
        rows={byRank ? SAMPLE_ROWS_BY_RANK : SAMPLE_ROWS}
        rowKey={(row) => row.id}
        height={VIRTUAL_TABLE_HEIGHT}
        label="Sample standings"
      />
    </>
  );
}

function DialogDemo() {
  const [open, setOpen] = useState(false);

  return (
    <>
      <div className="design-row">
        <Button
          variant="primary"
          onClick={() => {
            setOpen(true);
          }}
        >
          Open dialog
        </Button>
      </div>
      {open && (
        <Dialog
          title="Reverse this batch?"
          onClose={() => {
            setOpen(false);
          }}
          actions={
            <>
              <Button
                variant="secondary"
                onClick={() => {
                  setOpen(false);
                }}
              >
                Cancel
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  setOpen(false);
                }}
              >
                Write reversal
              </Button>
            </>
          }
        >
          The ledger is append-only. Reversing writes a new batch that points at this one; nothing is
          edited and nothing disappears.
        </Dialog>
      )}
    </>
  );
}

// A local inline glyph so the icon button has something to hold. Phosphor is the system's icon set
// and lands self-hosted with the first screen that needs a set of them; shipping the whole family to
// draw one plus sign would be the wrong trade.
function PlusIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}
