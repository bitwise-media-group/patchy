// Sign-in surface helpers. The server tells the SPA how (and whether)
// sign-in works through three small SPA-readable cookies; the session itself
// lives in HttpOnly cookies the client never touches:
//
//   patchy-auth-provider — {provider, authenticated, autoLogin} (base64url
//                          JSON). Absent means sign-in is not configured.
//   patchy-auth-error    — human-readable sign-in failure, shown once.
//   patchy-auth-logout   — marker set by sign-out; suppresses autoLogin.

export interface AuthProvider {
  provider: string;
  authenticated: boolean;
  autoLogin?: boolean;
}

const PROVIDER_COOKIE = "patchy-auth-provider";
const ERROR_COOKIE = "patchy-auth-error";
const LOGOUT_COOKIE = "patchy-auth-logout";

function readCookie(name: string): string | null {
  for (const part of document.cookie.split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name && rest.length) return rest.join("=");
  }
  return null;
}

function readJSONCookie<T>(name: string): T | null {
  const raw = readCookie(name);
  if (!raw) return null;
  try {
    return JSON.parse(atob(raw.replace(/-/g, "+").replace(/_/g, "/"))) as T;
  } catch {
    return null;
  }
}

function deleteCookie(name: string): void {
  document.cookie = `${name}=; path=/; max-age=0`;
}

// readProvider returns the sign-in surface descriptor, or null when the
// server has no authentication configured (rollups-only posture).
export function readProvider(): AuthProvider | null {
  const p = readJSONCookie<AuthProvider>(PROVIDER_COOKIE);
  return p && typeof p.provider === "string" ? p : null;
}

// readAuthError returns and clears the last sign-in failure.
export function readAuthError(): string | null {
  const msg = readJSONCookie<string>(ERROR_COOKIE);
  if (msg) deleteCookie(ERROR_COOKIE);
  return typeof msg === "string" && msg ? msg : null;
}

// consumeLogoutMarker reports (and clears) an explicit sign-out, so
// autoLogin pauses for one visit instead of bouncing straight back.
export function consumeLogoutMarker(): boolean {
  const present = readCookie(LOGOUT_COOKIE) !== null;
  if (present) deleteCookie(LOGOUT_COOKIE);
  return present;
}

// signInURL starts the server-side sign-in flow, returning here afterwards.
export function signInURL(): string {
  const here = location.pathname + location.search + location.hash;
  return `/oauth2/authorize?originalPath=${encodeURIComponent(here)}`;
}

// signOut posts the logout (POST-only, CSRF hardening) and reloads.
export async function signOut(): Promise<void> {
  try {
    await fetch("/logout", { method: "POST" });
  } finally {
    location.href = "/";
  }
}
