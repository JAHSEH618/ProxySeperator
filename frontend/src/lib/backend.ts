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

type WailsEvent<T = unknown> = {
  name: string;
  data: T;
  sender?: unknown;
};

declare global {
  interface Window {
    wails?: {
      Call?: {
        ByName: (methodName: string, ...args: unknown[]) => Promise<unknown>;
      };
      Events?: {
        On: (eventName: string, handler: (event: WailsEvent) => void) => () => void;
      };
    };
  }
}

type LoadedRuntime = {
  Call: {
    ByName: (methodName: string, ...args: unknown[]) => Promise<unknown>;
  };
  Events: {
    On: (eventName: string, handler: (event: WailsEvent) => void) => () => void;
  };
};

const SERVICE = "github.com/friedhelmliu/ProxySeperator/internal/app.BackendAPI";

function unwrapEventData<T>(payload: WailsEvent<T> | T): T {
  if (payload && typeof payload === "object" && "data" in payload) {
    return (payload as WailsEvent<T>).data;
  }
  return payload as T;
}

function getRuntime(): LoadedRuntime {
  const runtime = window.wails;
  if (!runtime?.Call?.ByName || !runtime.Events?.On) {
    throw new Error("Wails runtime 未加载");
  }
  return runtime as LoadedRuntime;
}

async function call<T>(method: string, ...args: unknown[]): Promise<T> {
  return (await getRuntime().Call.ByName(`${SERVICE}.${method}`, ...args)) as T;
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
    getRuntime().Events.On("runtime:status", (event) => handler(unwrapEventData(event) as RuntimeStatus)),
  onRuntimeHealth: (handler: (payload: HealthStatus) => void) =>
    getRuntime().Events.On("runtime:health", (event) => handler(unwrapEventData(event) as HealthStatus)),
  onRuntimeTraffic: (handler: (payload: TrafficStats) => void) =>
    getRuntime().Events.On("runtime:traffic", (event) => handler(unwrapEventData(event) as TrafficStats)),
  onRuntimeError: (handler: (payload: unknown) => void) =>
    getRuntime().Events.On("runtime:error", (event) => handler(unwrapEventData(event))),
  onRuntimeLog: (handler: (payload: LogEntry) => void) =>
    getRuntime().Events.On("runtime:log", (event) => handler(unwrapEventData(event) as LogEntry)),
};
