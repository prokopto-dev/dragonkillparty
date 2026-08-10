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
//
// `id` IS NOT AN ACCEPTED PROP on Input or TextArea, and that is the whole mechanism. An earlier
// version took `id ?? fieldId`, which reintroduced exactly the defect this component exists to
// prevent: `<Field label="Name"><Input id="name" /></Field>` pointed the label at the generated id
// and the input at "name", so the two were silently unassociated and the component still looked like
// it had handled it. Omitting the prop makes that combination a compile error rather than a defect a
// screenshot cannot show. props.test-d.tsx locks it.
//
// A control that needs to be referenced from elsewhere — an error message's aria-describedby, say —
// reads the id through useFieldId rather than supplying one.
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

/** The id Field generated for its control, for anything that must point at the control by id. */
export function useFieldId(): string | undefined {
  return useContext(FieldIdContext);
}

type InputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "className" | "id">;

export function Input(props: InputProps) {
  return <input id={useFieldId()} className="input" {...props} />;
}

type TextAreaProps = Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "className" | "id">;

export function TextArea(props: TextAreaProps) {
  return <textarea id={useFieldId()} className="input" {...props} />;
}
