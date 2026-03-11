import { Events } from "@wailsio/runtime";

import type {
  Config,
  HealthStatus,
  LogEntry,
  PreflightReport,
  RouteTestResult,
  RuleValidationResult,
  RuntimeStatus,
  TrafficStats,
} from "../types/backend";

declare global {
  interface Window {
    wails?: {
      Call: (method: string, ...args: unknown[]) => Promise<unknown>;
    };
  }
}

const SERVICE = "BackendAPI";

function ensureRuntime() {
  if (!window.wails?.Call) {
    throw new Error("Wails runtime 未初始化");
  }
}

async function call<T>(method: string, ...args: unknown[]): Promise<T> {
  ensureRuntime();
  return (await window.wails!.Call(`${SERVICE}.${method}`, ...args)) as T;
}

export const backend = {
  loadConfig: () => call<Config>("LoadConfig"),
  runPreflight: () => call<PreflightReport>("RunPreflight"),
  recoverNetwork: () => call<void>("RecoverNetwork"),
  saveConfig: (config: Config) => call<void>("SaveConfig", config),
  start: () => call<RuntimeStatus>("Start"),
  stop: () => call<void>("Stop"),
  restart: () => call<RuntimeStatus>("Restart"),
  getRuntimeStatus: () => call<RuntimeStatus>("GetRuntimeStatus"),
  getHealthStatus: () => call<HealthStatus>("GetHealthStatus"),
  getTrafficStats: () => call<TrafficStats>("GetTrafficStats"),
  testRoute: (input: string) => call<RouteTestResult>("TestRoute", input),
  validateRules: (lines: string[]) => call<RuleValidationResult>("ValidateRules", lines),
  listLogs: (limit = 20) => call<LogEntry[]>("ListLogs", limit),
  setLanguage: (language: string) => call<void>("SetLanguage", language),
};

export const runtimeEvents = {
  onRuntimeStatus: (handler: (payload: RuntimeStatus) => void) =>
    Events.On("runtime:status", (event) => handler(event.data as RuntimeStatus)),
  onRuntimeHealth: (handler: (payload: HealthStatus) => void) =>
    Events.On("runtime:health", (event) => handler(event.data as HealthStatus)),
  onRuntimeTraffic: (handler: (payload: TrafficStats) => void) =>
    Events.On("runtime:traffic", (event) => handler(event.data as TrafficStats)),
  onRuntimeError: (handler: (payload: unknown) => void) =>
    Events.On("runtime:error", (event) => handler(event.data)),
  onRuntimeLog: (handler: (payload: LogEntry) => void) =>
    Events.On("runtime:log", (event) => handler(event.data as LogEntry)),
};
