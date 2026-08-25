import { chatGPTAuthStatusClass, chatGPTAuthStatusLabel } from "./chatGPTAuthStatus";

const cases = [
  {
    status: { mode: "pillar", configured: true, authenticated: true } as const,
    label: "Connected via Pillar",
    className: "connected",
  },
  {
    status: {
      mode: "pillar",
      configured: true,
      authenticated: false,
      reauth_required: true,
    } as const,
    label: "Reauthentication required",
    className: "reauth-required",
  },
  {
    status: {
      mode: "pillar",
      configured: true,
      authenticated: false,
      error: "gateway unavailable",
    } as const,
    label: "Status unavailable",
    className: "unavailable",
  },
  {
    status: { mode: "standalone", configured: true, authenticated: false } as const,
    label: "Not connected",
    className: "standalone",
  },
];

for (const test of cases) {
  const label = chatGPTAuthStatusLabel(test.status);
  if (label !== test.label) throw new Error(`label = ${label}, want ${test.label}`);
  const className = chatGPTAuthStatusClass(test.status);
  if (className !== test.className) {
    throw new Error(`class = ${className}, want ${test.className}`);
  }
}
