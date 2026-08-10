import { useId, type ReactNode } from "react";

import "./Dialog.css";

// The system's `dialog-backdrop` + `dialog` classes, with the modal semantics the class names cannot
// carry: role, aria-modal, and an accessible name wired to the title element.
//
// The title is a <span>, matching the source system's markup — the dialog's name reaches assistive
// technology through aria-labelledby, so it does not also need to be a heading in the document
// outline.
//
// NOT here, deliberately: focus trapping, restore-on-close and Escape handling. Those need a real
// screen to be correct about (what receives focus on open depends on the dialog's content, and
// restore depends on what opened it), and a half-built trap is worse than an absent one because it
// looks handled. The first screen to open a dialog adds them; docs/design/09 §1 records that the
// accessibility primitives here are hand-built rather than pulled from a headless library.
export function Dialog({
  title,
  children,
  actions,
}: {
  title: ReactNode;
  children: ReactNode;
  actions?: ReactNode;
}) {
  const titleId = useId();

  return (
    <div className="dialog-backdrop">
      <div className="dialog" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <span className="dialog-title" id={titleId}>
          {title}
        </span>
        <div className="dialog-body">{children}</div>
        {actions !== undefined && <div className="dialog-actions">{actions}</div>}
      </div>
    </div>
  );
}
