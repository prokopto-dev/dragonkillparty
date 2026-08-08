import { createRootRoute, Outlet } from "@tanstack/react-router";

// The root route: the layout shell every page renders inside. TanStack Router v1, code-first route
// tree (no file-based route generation) so the scaffold has no build-time codegen step beyond the
// API client.
export const rootRoute = createRootRoute({
  component: RootLayout,
});

function RootLayout() {
  return (
    <div className="app-shell">
      <Outlet />
    </div>
  );
}
