/**
 * Keep organization context in ordinary navigation URLs. This is deliberately
 * separate from the active-scope store: a SuperAdmin may open a tenant from
 * the organization directory without changing their global operating scope.
 */
export function readOrgContext(search: string): string {
  return new URLSearchParams(search).get("org")?.trim() ?? "";
}

export function orgContextFromLocation(): string {
  return typeof window === "undefined" ? "" : readOrgContext(window.location.search);
}

export function buildOrgContextHref(path: string, orgID: string): string {
  return `${path}?${new URLSearchParams({ org: orgID }).toString()}`;
}
