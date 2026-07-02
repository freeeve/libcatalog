// Cross-screen reactive state: the signed-in session and the boot config.
import { writable } from "svelte/store";
import type { Session } from "./auth";
import type { ClientConfig } from "./types";

export const sessionStore = writable<Session | null>(null);
export const configStore = writable<ClientConfig>({ apiBase: "", localAuth: false, provider: "" });
