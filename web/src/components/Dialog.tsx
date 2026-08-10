import { useEffect, useId, useRef, type ReactNode } from "react";

import "./Dialog.css";

// The system's `dialog-backdrop` + `dialog` classes on a native <dialog> opened with showModal().
//
// THE ELEMENT CHOICE IS THE ACCESSIBILITY IMPLEMENTATION. An earlier version rendered two divs with
// role="dialog" and aria-modal="true" and deferred focus trapping, Escape and focus restoration to
// "the first screen that needs them". That is not a deferral, it is a false claim: aria-modal tells
// assistive technology the rest of the page is inert while keyboard focus walked straight through it,
// Escape did nothing, and focus neither moved on open nor came back on close. A primitive that
// announces a guarantee it does not implement is worse than one that announces nothing, because every
// screen built on it inherits the lie.
//
// showModal() on a native <dialog> gets all of it from the platform, and none of it from code that
// would need its own test to be trustworthy:
//
//   - the dialog enters the TOP LAYER, so everything behind it is genuinely inert — not merely
//     described as inert. aria-modal is therefore not set: modality is real, so asserting it is
//     redundant, and on some screen readers asserting it hides content that the top layer already
//     handles correctly.
//   - focus moves into the dialog on open and RETURNS to the previously focused element on close.
//   - Escape fires `cancel`, which is where onClose is wired.
//
// The mockups draw this as divs because mockups/nocturne/styles.css is plain CSS on plain HTML with
// no JavaScript — a static page cannot call showModal(). The class names and every computed
// declaration are unchanged, so the look is faithful; the behaviour is correct where a static mockup
// could not express it. Dialog.css carries the UA resets that keeps the two identical.
//
// Backdrop-click-to-dismiss is deliberately absent: no mockup specifies it, and dismissing a
// half-filled officer form on a stray click loses work. Adding it is a design decision.
export function Dialog({
  title,
  children,
  actions,
  onClose,
}: {
  title: ReactNode;
  children: ReactNode;
  actions?: ReactNode;
  /** Called when the user asks to close — Escape today. The caller then unmounts the Dialog. */
  onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const el = ref.current;
    if (el === null) {
      return undefined;
    }

    if (!el.open) {
      el.showModal();
    }

    // Closing on unmount is what returns focus to whatever opened the dialog. Without it a dialog
        // removed from the tree while still `open` leaves focus on a detached node.
    return () => {
      if (el.open) {
        el.close();
      }
    };
  }, []);

  return (
    <dialog
      ref={ref}
      className="dialog-backdrop"
      aria-labelledby={titleId}
      // preventDefault keeps the element open and hands the decision to the caller, so closing always
      // travels the same path — onClose, then the caller unmounts — whether it came from Escape or
      // from a button. Letting the platform close it here as well would fire `close` during unmount
      // and call onClose twice.
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <div className="dialog">
        <span className="dialog-title" id={titleId}>
          {title}
        </span>
        <div className="dialog-body">{children}</div>
        {actions !== undefined && <div className="dialog-actions">{actions}</div>}
      </div>
    </dialog>
  );
}
