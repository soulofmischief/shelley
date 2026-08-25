import type { ChatGPTAuthStatus } from "../../services/api";

export function chatGPTAuthStatusLabel(status: ChatGPTAuthStatus): string {
  if (status.mode === "pillar") {
    if (status.error) return "Status unavailable";
    if (status.authenticated) return "Connected via Pillar";
    if (status.reauth_required) return "Reauthentication required";
    return "Managed by Pillar";
  }
  return status.authenticated ? "Connected" : "Not connected";
}

export function chatGPTAuthStatusClass(status: ChatGPTAuthStatus): string {
  if (status.authenticated) return "connected";
  if (status.reauth_required) return "reauth-required";
  if (status.error) return "unavailable";
  return status.mode;
}
