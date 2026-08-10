import {
  createContext,
  useContext,
  useId,
  type InputHTMLAttributes,
  type ReactNode,
  type TextareaHTMLAttributes,
} from "react";

import "./Field.css";

// The system's `field` + `label` + `input` classes.
//
// Field generates the id and Input/TextArea read it, so a label is ALWAYS associated with its
// control. Requiring the caller to keep an `htmlFor` and an `id` in sync by hand is the single most
// repeated accessibility defect in a forms-heavy console, and there are ~55 screens of forms coming:
// the association is the component's job, not the screen author's.
const FieldIdContext = createContext<string | undefined>(undefined);

export function Field({ label, children }: { label: ReactNode; children: ReactNode }) {
  const id = useId();

  return (
    <FieldIdContext.Provider value={id}>
      <div className="field">
        <label htmlFor={id}>{label}</label>
        {children}
      </div>
    </FieldIdContext.Provider>
  );
}

type InputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "className">;

export function Input({ id, ...rest }: InputProps) {
  const fieldId = useContext(FieldIdContext);

  return <input id={id ?? fieldId} className="input" {...rest} />;
}

type TextAreaProps = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "className">;

export function TextArea({ id, ...rest }: TextAreaProps) {
  const fieldId = useContext(FieldIdContext);

  return <textarea id={id ?? fieldId} className="input" {...rest} />;
}
