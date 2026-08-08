import { createRouter } from "@tanstack/react-router";

import { indexRoute } from "./routes/index";
import { rootRoute } from "./routes/root";

// The route tree. Code-first: routes register their parent explicitly, so there is no generated
// routeTree.gen.ts to keep in sync. One entry today (the landing page); feature routes append here.
const routeTree = rootRoute.addChildren([indexRoute]);

export const router = createRouter({ routeTree });

// TanStack Router wants the router type registered globally so useParams/useSearch are typed. This
// is the one module augmentation the scaffold carries.
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
