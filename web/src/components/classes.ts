// classes joins a base class with conditional modifiers, dropping the false and undefined ones.
//
// The whole of it, because the alternative is worse in both directions: a template literal per
// component drifts (one forgets a space, another emits "btn undefined"), and a classname library is
// a dependency for four lines. Nocturne's components are flat classes plus modifiers, so this is the
// only shape any of them needs.
export function classes(...parts: (string | false | undefined)[]): string {
  return parts.filter((part): part is string => typeof part === "string" && part !== "").join(" ");
}
