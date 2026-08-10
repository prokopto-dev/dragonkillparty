import { createContext, useContext, useId, type InputHTMLAttributes, type ReactNode } from "react";

import "./Seg.css";

// The system's `seg` + `seg-opt` classes: a segmented control built on a native radio group, so
// arrow-key navigation, the group's accessible name and the checked state all come from the platform.
//
// SEG OWNS THE GROUP NAME. A radio group is a group only because its inputs share a `name` — that one
// attribute is what makes arrow keys traverse the segment and what makes checking one option uncheck
// the others. An earlier version left `name` as an optional pass-through on each SegOption, so
// `<Seg><SegOption>A</SegOption><SegOption>B</SegOption></Seg>` rendered `role="group"` around
// unrelated radios: both could be checked at once and arrow keys did nothing. The API promised a group
// and produced a pile of checkboxes wearing radio styling.
//
// So Seg generates one name and SegOption consumes it, and `name` is not an accepted prop on
// SegOption. props.test-d.tsx locks that. Nothing a screen author can pass creates a broken segment.
//
// State stays with the caller: a segmented control in this product selects a server-side filter or a
// layout mode, and both are owned by the route (search params) or by a settings resource.
const SegNameContext = createContext<string | undefined>(undefined);

export function Seg({ children, label }: { children: ReactNode; label: string }) {
  const name = useId();

  return (
    <SegNameContext.Provider value={name}>
      <div className="seg" role="group" aria-label={label}>
        {children}
      </div>
    </SegNameContext.Provider>
  );
}

type SegOptionProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "className" | "type" | "name"
> & {
  children: ReactNode;
};

export function SegOption({ children, ...rest }: SegOptionProps) {
  const name = useContext(SegNameContext);

  return (
    <label className="seg-opt">
      <input type="radio" name={name} {...rest} />
      {children}
    </label>
  );
}
