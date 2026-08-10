import type { InputHTMLAttributes, ReactNode } from "react";

import "./Radio.css";

// The system's `radio` + `dot` classes. The <span class="dot"> is the ADJACENT SIBLING of the input,
// which the CSS depends on (`input:checked + .dot`): nothing may be inserted between them.
//
// `name` is REQUIRED, where React's own InputHTMLAttributes makes it optional. Radios are a group
// only because they share a name, and a set of unnamed radios is a set of independent controls that
// can all be checked at once — a defect that looks like nothing in a screenshot. Unlike Seg, this
// primitive cannot generate the name itself, because the caller decides which radios form the group;
// so the next best thing is to make forgetting it impossible.
type RadioProps = Omit<InputHTMLAttributes<HTMLInputElement>, "className" | "type" | "name"> & {
  children: ReactNode;
  name: string;
};

export function Radio({ children, ...rest }: RadioProps) {
  return (
    <label className="radio">
      <input type="radio" {...rest} />
      <span className="dot" />
      <span>{children}</span>
    </label>
  );
}
