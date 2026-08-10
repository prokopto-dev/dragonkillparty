import { createRouter } from "@tanstack/react-router";

import { designRoute } from "./routes/design";
import { indexRoute } from "./routes/index";
import { rootRoute } from "./routes/root";

// The route tree. Code-first: routes register their parent explicitly, so there is no generated
// routeTree.gen.ts to keep in sync. Feature routes append here.
//
// designRoute (/_design) is the design-system fixture: every token and every base component class,
// rendered. It is imported STATICALLY, which puts it in the initial-route chunk on purpose — that is
// what makes `make budget-bundle` measure the shell verify-before-phase-0 V13 specifies (React +
// Router + Query + Virtual + a 12-column virtualised table) on every PR. Converting it to a lazy
// route means re-deriving web/bundle-budget.json in the same change.
const routeTree = rootRoute.addChildren([indexRoute, designRoute]);

export const router = createRouter({ routeTree });

// TanStack Router wants the router type registered globally so useParams/useSearch are typed. This
// is the one module augmentation the scaffold carries.
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
