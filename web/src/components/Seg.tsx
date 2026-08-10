import type { InputHTMLAttributes, ReactNode } from "react";

import "./Seg.css";

// The system's `seg` + `seg-opt` classes: a segmented control built on a native radio group, so
// arrow-key navigation, the group's accessible name and the checked state all come from the platform.
//
// State stays with the caller. A segmented control in this product selects a server-side filter or a
// layout mode, and both of those are owned by the route (search params) or by a settings resource,
// never by the control.
export function Seg({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {children}
    </div>
  );
}

type SegOptionProps = Omit<InputHTMLAttributes<HTMLInputElement>, "className" | "type"> & {
  children: ReactNode;
};

export function SegOption({ children, ...rest }: SegOptionProps) {
  return (
    <label className="seg-opt">
      <input type="radio" {...rest} />
      {children}
    </label>
  );
}
