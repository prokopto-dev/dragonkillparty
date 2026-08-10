import type { ButtonHTMLAttributes, ReactNode } from "react";

import "./Button.css";

import { classes } from "./classes";

// The system's `btn` class and its five modifiers (.claude/rules/design-system.md §Components).
// Reproduce these names; do not invent parallel ones.
//
// `variant` is a closed union rather than a free className because "buttons are outlined, never
// filled" is only enforceable if the component owns the set. There is no `filled`, and adding one is
// a design change.
export type ButtonVariant = "primary" | "secondary" | "ghost";

// A lookup rather than `btn-${variant}`. Two reasons, both practical: a typo in a union member is a
// compile error where a typo in a template is a class that silently does nothing, and the class names
// stay greppable — test/repo/design_tokens_test.go requires every class the stylesheets define to
// appear as a literal somewhere in web/src, which is how an orphaned CSS class gets noticed.
const VARIANT_CLASS: Record<ButtonVariant, string> = {
  primary: "btn-primary",
  secondary: "btn-secondary",
  ghost: "btn-ghost",
};

type ButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className"> & {
  children: ReactNode;
  variant?: ButtonVariant;
  /** A square button holding a single icon. The accessible name must come from aria-label. */
  icon?: boolean;
  /** Full width, for a form's terminal action. */
  block?: boolean;
};

export function Button({ children, variant, icon, block, type, ...rest }: ButtonProps) {
  return (
    <button
      // A <button> inside a <form> submits by default, which fires a full-page navigation and throws
      // away the SPA. Default to "button" and make submitting explicit.
      type={type ?? "button"}
      className={classes(
        "btn",
        variant !== undefined && VARIANT_CLASS[variant],
        icon === true && "btn-icon",
        block === true && "btn-block",
      )}
      {...rest}
    >
      {children}
    </button>
  );
}
