// Every outbound destination lives here. The page repeats its two calls to
// action five times over; one constant each is what keeps them from drifting.

export const REPO_URL = "https://github.com/blackprince001/flowbench";
export const DOCS_URL = "https://blackprince001.gitbook.io/flowbench-documentation";

export const RELEASES_URL = `${REPO_URL}/releases`;
export const LICENSE_URL = `${REPO_URL}/blob/main/LICENSE`;

// Bumped with the tag. The label stays evergreen so only one of these two has
// to be remembered at release time.
export const RELEASE_TAG = "v0.1.1";
export const RELEASE_TEXT = "Latest release";

// Primary is the repository. There is no onboarding surface to send anyone to,
// so the secondary action hands off to the hosted docs instead of promising one.
export const PRIMARY_CTA = { label: "View on GitHub", href: REPO_URL } as const;
export const SECONDARY_CTA = { label: "View docs", href: DOCS_URL } as const;

export const NAV_LINKS = [
  { label: "Product", href: "#product" },
  { label: "Features", href: "#features" },
  { label: "Workflow", href: "#workflow" },
  { label: "Docs", href: DOCS_URL, external: true },
] as const;

export const SITE = {
  // Origin only — canonical and OG URLs are built from it. Deployment target is
  // still open (Task 0); a GitHub project-pages deploy would serve under
  // /flowbench and needs `base` set in astro.config.mjs to match.
  url: "https://blackprince001.github.io",
  title: "Flowbench — one flow, every test profile",
  description:
    "Author API journeys once in YAML or Python. Run them from integration to stress and soak, then see exactly where the time — and the failure — went.",
} as const;
