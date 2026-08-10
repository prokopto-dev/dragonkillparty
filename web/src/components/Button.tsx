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

type ButtonBase = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className"> & {
  children: ReactNode;
  variant?: ButtonVariant;
  /** Full width, for a form's terminal action. */
  block?: boolean;
};

// `icon` DISCRIMINATES, and requiring aria-label on that branch is the point.
//
// An icon button's children are a glyph, and a glyph carries no accessible name — the SVG is
// aria-hidden, so without aria-label the control announces as "button" and nothing else. With
// `icon?: boolean` the type said nothing about that, and `<Button icon><PlusIcon /></Button>`
// compiled into a nameless control; the fixture happened to pass aria-label, which is exactly how a
// gap like this stays invisible. Found in review.
//
// Every other accessibility guarantee in this directory is enforced by types rather than documented
// (Field owns the control id, Seg owns the group name, Radio requires a name, Dialog requires
// onClose). This one now is too. props.test-d.tsx locks it.
type ButtonProps =
  | (ButtonBase & {
      /** A square button holding a single icon. Its accessible name comes from aria-label. */
      icon: true;
      "aria-label": string;
    })
  | (ButtonBase & { icon?: false });

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
