import type { InputHTMLAttributes, ReactNode } from "react";

import "./Radio.css";

// The system's `radio` + `dot` classes. The <span class="dot"> is the ADJACENT SIBLING of the input,
// which the CSS depends on (`input:checked + .dot`): nothing may be inserted between them.
type RadioProps = Omit<InputHTMLAttributes<HTMLInputElement>, "className" | "type"> & {
  children: ReactNode;
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
