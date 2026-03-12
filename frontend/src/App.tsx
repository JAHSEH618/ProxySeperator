import { useEffect, useRef, useState } from "react";

import { backend, runtimeEvents } from "./lib/backend";
import type {
  Config,
  HealthStatus,
  LogEntry,
  PreflightReport,
  RouteTestResult,
  RuleSummary,
  RuleValidationResult,
  RuntimeStatus,
  TrafficStats,
  UpstreamHealth,
} from "./types/backend";

const sampleRules = [
  "DOMAIN-SUFFIX,company.com",
  "DOMAIN-SUFFIX,internal.corp",
  "DOMAIN,jira.mycompany.io",
  "DOMAIN,git.mycompany.io",
  "DOMAIN-KEYWORD,corpnet",
  "IP-CIDR,10.0.0.0/8",
  "IP-CIDR,172.16.0.0/12",
  "IP-CIDR,192.168.0.0/16",
];

const defaultConfig: Config = {
  version: 1,
  companyUpstream: { host: "system-route", port: 0, protocol: "direct" },
  personalUpstream: { host: "127.0.0.1", port: 7897, protocol: "auto" },
  rules: [".company.com", ".internal", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"],
  advanced: {
    mode: "system",
    tunEnabled: false,
    personalTUNMode: false,
    udpForwarding: false,
    bypassChinaIP: false,
    autoStart: false,
    startMinimized: false,
  },
  ui: { language: "zh-CN", theme: "system" },
};

type NoticeTone = "info" | "success" | "warning" | "error";

type Notice = {
  tone: NoticeTone;
  text: string;
};

type LocalRuleSummary = {
  total: number;
  domainSuffix: number;
  domainExact: number;
  domainKeyword: number;
  cidr: number;
};

type RawLogEntry = Partial<LogEntry> & {
  Timestamp?: string;
  Level?: string;
  Module?: string;
  Message?: string;
  Fields?: Record<string, unknown>;
};

type RawUpstreamHealth = Partial<UpstreamHealth> & {
  Reachable?: boolean;
  Protocol?: string;
  RTTMs?: number;
  LastSuccessAt?: string;
  ConsecutiveFailures?: number;
};

type RawHealthStatus = Partial<HealthStatus> & {
  CheckedAt?: string;
  Company?: RawUpstreamHealth;
  Personal?: RawUpstreamHealth;
};

type RawRuntimeStatus = Partial<RuntimeStatus> & {
  State?: string;
  Mode?: string;
  RequestedMode?: string;
  ModeReason?: string;
  RecoveryRequired?: boolean;
  StartedAt?: string;
  UptimeSeconds?: number;
  LastErrorCode?: string;
  LastErrorMessage?: string;
};

type IconName =
  | "activity"
  | "chevronDown"
  | "chevronUp"
  | "copy"
  | "logs"
  | "play"
  | "route"
  | "save"
  | "settings"
  | "stop";

function serializeConfig(config: Config) {
  return JSON.stringify(config);
}

function splitLines(value: string) {
  return value.replace(/\r\n/g, "\n").split("\n");
}

function sanitizePort(raw: string) {
  const parsed = Number(raw);
  if (!Number.isFinite(parsed)) {
    return 0;
  }
  return Math.max(0, Math.min(65535, Math.trunc(parsed)));
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let current = value;
  let index = 0;
  while (current >= 1024 && index < units.length - 1) {
    current /= 1024;
    index += 1;
  }
  const digits = current >= 100 || index === 0 ? 0 : 1;
  return `${current.toFixed(digits)} ${units[index]}`;
}

function formatRate(value: number) {
  return `${formatBytes(value)}/s`;
}

function formatDuration(value: number) {
  const total = Math.max(0, Math.trunc(value));
  const hours = Math.floor(total / 3600)
    .toString()
    .padStart(2, "0");
  const minutes = Math.floor((total % 3600) / 60)
    .toString()
    .padStart(2, "0");
  const seconds = Math.floor(total % 60)
    .toString()
    .padStart(2, "0");
  return `${hours}:${minutes}:${seconds}`;
}

function formatTimestamp(value?: string) {
  if (!value) {
    return "--";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "--";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function formatProtocol(value?: string) {
  switch ((value ?? "").toLowerCase()) {
    case "direct":
      return "DIRECT";
    case "http":
      return "HTTP";
    case "socks5":
      return "SOCKS5";
    case "unknown":
      return "未知";
    case "auto":
      return "AUTO";
    default:
      return "AUTO";
  }
}

function formatModeLabel(value?: string) {
  return value === "tun" ? "TUN 模式" : "系统代理";
}

function formatModeBadge(value?: string) {
  return value === "tun" ? "TUN Mode" : "System Proxy";
}

function formatRuntimeState(value: string) {
  switch (value) {
    case "running":
      return "运行中";
    case "starting":
      return "启动中";
    case "stopping":
      return "停止中";
    case "error":
      return "异常";
    default:
      return "未启动";
  }
}

function formatRouteTarget(value?: string) {
  switch (value) {
    case "company":
      return "公司网络";
    case "direct":
      return "直连";
    default:
      return "个人代理";
  }
}

function formatRuleType(value?: string) {
  switch (value) {
    case "DOMAIN_SUFFIX":
      return "域名后缀";
    case "DOMAIN_EXACT":
      return "完整域名";
    case "DOMAIN_KEYWORD":
      return "关键词";
    case "IP_CIDR":
      return "CIDR";
    case "LOCAL_IP":
      return "本地地址";
    default:
      return "默认规则";
  }
}

function summarizeRules(lines: string[]): LocalRuleSummary {
  const summary: LocalRuleSummary = {
    total: 0,
    domainSuffix: 0,
    domainExact: 0,
    domainKeyword: 0,
    cidr: 0,
  };

  for (const raw of lines) {
    const trimmed = raw.trim();
    if (!trimmed || trimmed.startsWith("#")) {
      continue;
    }
    summary.total += 1;

    const normalized = trimmed.toLowerCase();
    const [prefix, rest] = normalized.includes(",")
      ? normalized.split(",", 2)
      : ["", normalized];

    if (prefix === "domain-suffix" || prefix === "domain_suffix" || normalized.startsWith(".")) {
      summary.domainSuffix += 1;
      continue;
    }
    if (prefix === "domain") {
      summary.domainExact += 1;
      continue;
    }
    if (prefix === "domain-keyword" || prefix === "domain_keyword") {
      summary.domainKeyword += 1;
      continue;
    }
    if (prefix === "ip-cidr" || prefix === "ip_cidr" || /^\d+\.\d+\.\d+\.\d+\/\d+$/.test(rest)) {
      summary.cidr += 1;
      continue;
    }
    if (normalized.includes(".")) {
      summary.domainExact += 1;
      continue;
    }
    summary.domainKeyword += 1;
  }

  return summary;
}

function healthHint(health: UpstreamHealth | undefined, fallbackProtocol: string) {
  const protocol = (health?.protocol || fallbackProtocol || "").toLowerCase();
  if (protocol === "direct") {
    return "复用系统路由 / 公司 VPN";
  }
  if (!health) {
    return `等待探测 · ${formatProtocol(fallbackProtocol)}`;
  }
  if (!health.reachable) {
    return `端口不可达 · ${formatProtocol(health.protocol || fallbackProtocol)}`;
  }
  const rttText = health.rttMs > 0 ? ` · ${health.rttMs} ms` : "";
  return `在线 · ${formatProtocol(health.protocol || fallbackProtocol)}${rttText}`;
}

function healthStateLabel(runtimeState: string, health: UpstreamHealth | undefined) {
  if (runtimeState === "starting") {
    return "Checking";
  }
  if (health?.reachable) {
    return "Online";
  }
  return runtimeState === "running" ? "Degraded" : "Offline";
}

function noticeFromError(error: unknown): Notice {
  return {
    tone: "error",
    text: error instanceof Error ? error.message : String(error),
  };
}

function ruleSummaryFromValidation(summary: RuleSummary): LocalRuleSummary {
  return {
    total: summary.total,
    domainSuffix: summary.domainSuffix,
    domainExact: summary.domainExact,
    domainKeyword: summary.domainKeyword,
    cidr: summary.cidr,
  };
}

function firstBlockingCheck(report: PreflightReport | null) {
  return report?.checks.find((item) => item.status === "fail") ?? null;
}

function formatPreflightMode(value?: string) {
  return value === "tun" ? "TUN 共存模式" : "系统代理模式";
}

function ensureBypassCIDRs(lines: string[]) {
  const required = ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"];
  const existing = new Set(lines.map((line) => line.trim().toLowerCase()).filter(Boolean));
  const next = [...lines];
  for (const cidr of required) {
    if (!existing.has(cidr.toLowerCase())) {
      next.push(cidr);
    }
  }
  return next;
}

function normalizeConfig(value: Config): Config {
  return {
    ...defaultConfig,
    ...value,
    companyUpstream: {
      ...defaultConfig.companyUpstream,
      ...value.companyUpstream,
    },
    personalUpstream: {
      ...defaultConfig.personalUpstream,
      ...value.personalUpstream,
    },
    advanced: {
      ...defaultConfig.advanced,
      ...value.advanced,
    },
    ui: {
      ...defaultConfig.ui,
      ...value.ui,
    },
    rules: Array.isArray(value.rules) ? value.rules : defaultConfig.rules,
  };
}

function pickString(...values: unknown[]) {
  for (const value of values) {
    if (typeof value === "string") {
      return value;
    }
  }
  return "";
}

function pickBoolean(...values: unknown[]) {
  for (const value of values) {
    if (typeof value === "boolean") {
      return value;
    }
  }
  return false;
}

function pickNumber(...values: unknown[]) {
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return 0;
}

function pickRecord(...values: unknown[]) {
  for (const value of values) {
    if (value && typeof value === "object" && !Array.isArray(value)) {
      return value as Record<string, unknown>;
    }
  }
  return undefined;
}

function normalizeLogEntry(entry: unknown): LogEntry | null {
  if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
    return null;
  }

  const raw = entry as RawLogEntry;
  const timestamp = pickString(raw.timestamp, raw.Timestamp).trim();
  const level = pickString(raw.level, raw.Level).trim().toUpperCase() || "INFO";
  const module = pickString(raw.module, raw.Module).trim() || "app";
  const message = pickString(raw.message, raw.Message).trim();
  const fields = pickRecord(raw.fields, raw.Fields);

  if (!timestamp && !message && !fields && !pickString(raw.level, raw.Level, raw.module, raw.Module)) {
    return null;
  }

  return {
    timestamp,
    level,
    module,
    message,
    fields,
  };
}

function normalizeLogEntries(entries: unknown): LogEntry[] {
  if (!Array.isArray(entries)) {
    return [];
  }
  return entries
    .map((entry) => normalizeLogEntry(entry))
    .filter((entry): entry is LogEntry => entry !== null);
}

function normalizeUpstreamHealth(value: unknown): UpstreamHealth {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return {
      reachable: false,
      protocol: "unknown",
      rttMs: 0,
      consecutiveFailures: 0,
    };
  }

  const raw = value as RawUpstreamHealth;
  const lastSuccessAt = pickString(raw.lastSuccessAt, raw.LastSuccessAt).trim();

  return {
    reachable: pickBoolean(raw.reachable, raw.Reachable),
    protocol: pickString(raw.protocol, raw.Protocol).trim() || "unknown",
    rttMs: pickNumber(raw.rttMs, raw.RTTMs),
    lastSuccessAt: lastSuccessAt || undefined,
    consecutiveFailures: pickNumber(raw.consecutiveFailures, raw.ConsecutiveFailures),
  };
}

function normalizeHealthStatus(value: unknown): HealthStatus | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }

  const raw = value as RawHealthStatus;
  const checkedAt = pickString(raw.checkedAt, raw.CheckedAt).trim();

  return {
    checkedAt: checkedAt || undefined,
    company: normalizeUpstreamHealth(raw.company ?? raw.Company),
    personal: normalizeUpstreamHealth(raw.personal ?? raw.Personal),
  };
}

function normalizeRuntimeStatus(value: unknown): RuntimeStatus {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { state: "idle", mode: "system", uptimeSeconds: 0 };
  }

  const raw = value as RawRuntimeStatus;
  const startedAt = pickString(raw.startedAt, raw.StartedAt).trim();
  const lastErrorCode = pickString(raw.lastErrorCode, raw.LastErrorCode).trim();
  const lastErrorMessage = pickString(raw.lastErrorMessage, raw.LastErrorMessage).trim();

  return {
    state: pickString(raw.state, raw.State).trim() || "idle",
    mode: pickString(raw.mode, raw.Mode).trim() || "system",
    requestedMode: pickString(raw.requestedMode, raw.RequestedMode).trim() || undefined,
    modeReason: pickString(raw.modeReason, raw.ModeReason).trim() || undefined,
    recoveryRequired: pickBoolean(raw.recoveryRequired, raw.RecoveryRequired),
    startedAt: startedAt || undefined,
    uptimeSeconds: pickNumber(raw.uptimeSeconds, raw.UptimeSeconds),
    lastErrorCode: lastErrorCode || undefined,
    lastErrorMessage: lastErrorMessage || undefined,
  };
}

function Icon({ name, className }: { name: IconName; className?: string }) {
  switch (name) {
    case "activity":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M3 12h4l2.5-6 4 12 2.5-6H21" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "chevronDown":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="m6 9 6 6 6-6" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "chevronUp":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="m6 15 6-6 6 6" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "copy":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <rect x="9" y="9" width="10" height="10" rx="2" />
          <path d="M5 15V7a2 2 0 0 1 2-2h8" strokeLinecap="round" />
        </svg>
      );
    case "logs":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01" strokeLinecap="round" />
        </svg>
      );
    case "play":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="currentColor">
          <path d="M8 6.5v11l9-5.5-9-5.5Z" />
        </svg>
      );
    case "route":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M5 6h8a3 3 0 1 1 0 6H7a3 3 0 1 0 0 6h12" strokeLinecap="round" strokeLinejoin="round" />
          <path d="m15 16 4 2-4 2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "save":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M5 4h11l3 3v13H5z" strokeLinejoin="round" />
          <path d="M8 4v6h8V4M9 19h6" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "settings":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
          <path d="M10.3 4.3 9.7 6a6.8 6.8 0 0 0-1.5.9L6.5 6.2 5 7.7l.7 1.7c-.4.5-.7 1-.9 1.5l-1.7.6v2l1.7.6c.2.5.5 1 .9 1.5L5 17.3l1.5 1.5 1.7-.7c.5.4 1 .7 1.5.9l.6 1.7h2l.6-1.7c.5-.2 1-.5 1.5-.9l1.7.7 1.5-1.5-.7-1.7c.4-.5.7-1 .9-1.5l1.7-.6v-2l-1.7-.6a6.8 6.8 0 0 0-.9-1.5l.7-1.7-1.5-1.5-1.7.7a6.8 6.8 0 0 0-1.5-.9l-.6-1.7z" strokeLinejoin="round" />
          <circle cx="12" cy="12" r="2.8" />
        </svg>
      );
    case "stop":
      return (
        <svg className={className} viewBox="0 0 24 24" fill="currentColor">
          <rect x="7" y="7" width="10" height="10" rx="2" />
        </svg>
      );
    default:
      return null;
  }
}

function HeaderStatusChip({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone: "company" | "personal" | "neutral";
}) {
  return (
    <div className={`header-status header-status--${tone}`}>
      <span className="header-status__dot" />
      <span className="header-status__label">{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function TrafficCard({
  accent,
  title,
  health,
  runtimeState,
  totalLabel,
  totalValue,
  rateLabel,
  rateValue,
}: {
  accent: "company" | "personal";
  title: string;
  health: UpstreamHealth | undefined;
  runtimeState: string;
  totalLabel: string;
  totalValue: string;
  rateLabel: string;
  rateValue: string;
}) {
  return (
    <section className={`traffic-card traffic-card--${accent}`}>
      <div className="traffic-card__header">
        <div className="traffic-card__title">
          <span className="traffic-card__dot" />
          <h3>{title}</h3>
        </div>
        <span className={`traffic-card__badge ${health?.reachable ? "is-online" : "is-offline"}`}>
          {healthStateLabel(runtimeState, health)}
        </span>
      </div>
      <div className="traffic-card__metrics">
        <div>
          <span>{totalLabel}</span>
          <strong>{totalValue}</strong>
        </div>
        <div>
          <span>{rateLabel}</span>
          <strong>{rateValue}</strong>
        </div>
      </div>
      <p className="traffic-card__meta">{healthHint(health, "auto")}</p>
    </section>
  );
}

function SettingCard({
  title,
  description,
  checked,
  disabled,
  onToggle,
}: {
  title: string;
  description: string;
  checked: boolean;
  disabled?: boolean;
  onToggle: () => void;
}) {
  return (
    <div className={`setting-card ${disabled ? "is-disabled" : ""}`}>
      <div className="setting-card__copy">
        <h3>{title}</h3>
        <p>{description}</p>
      </div>
      <button
        className={`switch ${checked ? "is-on" : ""}`}
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={onToggle}
        disabled={disabled}
      >
        <span className="switch__thumb" />
      </button>
    </div>
  );
}

export function App() {
  const [config, setConfig] = useState<Config>(defaultConfig);
  const [savedConfigKey, setSavedConfigKey] = useState(serializeConfig(defaultConfig));
  const [runtimeStatus, setRuntimeStatus] = useState<RuntimeStatus>({
    state: "idle",
    mode: "system",
    uptimeSeconds: 0,
  });
  const [health, setHealth] = useState<HealthStatus | null>(null);
  const [traffic, setTraffic] = useState<TrafficStats | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [testInput, setTestInput] = useState("git.company.com");
  const [testResult, setTestResult] = useState<RouteTestResult | null>(null);
  const [validation, setValidation] = useState<RuleValidationResult | null>(null);
  const [preflight, setPreflight] = useState<PreflightReport | null>(null);
  const [validatedRulesKey, setValidatedRulesKey] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);
  const [advancedExpanded, setAdvancedExpanded] = useState(true);
  const [routeTesterOpen, setRouteTesterOpen] = useState(false);
  const [isBootstrapping, setIsBootstrapping] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [isStarting, setIsStarting] = useState(false);
  const [isStopping, setIsStopping] = useState(false);
  const [isRecovering, setIsRecovering] = useState(false);
  const [isValidating, setIsValidating] = useState(false);
  const [isTestingRoute, setIsTestingRoute] = useState(false);
  const [uiRuntimeActive, setUiRuntimeActive] = useState(false);
  const logSectionRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const applyHealthStatus = (payload: unknown) => {
      setHealth(normalizeHealthStatus(payload));
    };

    const applyRuntimeStatus = (payload: unknown) => {
      const normalized = normalizeRuntimeStatus(payload);
      setRuntimeStatus(normalized);
      setUiRuntimeActive(normalized.state === "running" || normalized.state === "starting");
    };

    const appendLogEntry = (entry: unknown) => {
      const normalized = normalizeLogEntry(entry);
      if (!normalized) {
        return;
      }
      setLogs((current) => [...current, normalized].slice(-50));
    };

    const disposers = [
      runtimeEvents.onRuntimeStatus(applyRuntimeStatus),
      runtimeEvents.onRuntimeHealth(applyHealthStatus),
      runtimeEvents.onRuntimeTraffic(setTraffic),
      runtimeEvents.onRuntimeLog(appendLogEntry),
      runtimeEvents.onRuntimeError((payload) => {
        setNotice({
          tone: "error",
          text: typeof payload === "string" ? payload : "运行时出现错误",
        });
      }),
    ];

    void bootstrap();
    return () => {
      disposers.forEach((dispose) => dispose());
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setInterval(() => {
      void (async () => {
        try {
          const nextRuntimeStatus = await backend.getRuntimeStatus();
          if (!cancelled) {
            const normalized = normalizeRuntimeStatus(nextRuntimeStatus);
            setRuntimeStatus(normalized);
            setUiRuntimeActive(normalized.state === "running" || normalized.state === "starting");
          }
        } catch {
          // Ignore transient polling failures; explicit actions will surface errors.
        }
      })();
    }, 1500);

    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  async function bootstrap() {
    setIsBootstrapping(true);
    try {
      const loadedConfig = normalizeConfig(await backend.loadConfig());
      const [nextRuntimeStatus, nextHealth, nextTraffic, nextLogs, nextPreflight] = await Promise.all([
        backend.getRuntimeStatus(),
        backend.getHealthStatus(),
        backend.getTrafficStats(),
        backend.listLogs(20),
        backend.runPreflight(),
      ]);
      const normalizedRuntimeStatus = normalizeRuntimeStatus(nextRuntimeStatus);

      setConfig(loadedConfig);
      setSavedConfigKey(serializeConfig(loadedConfig));
      setRuntimeStatus(normalizedRuntimeStatus);
      setUiRuntimeActive(normalizedRuntimeStatus.state === "running" || normalizedRuntimeStatus.state === "starting");
      setHealth(normalizeHealthStatus(nextHealth));
      setTraffic(nextTraffic);
      setLogs(normalizeLogEntries(nextLogs));
      setPreflight(nextPreflight);
      setNotice({
        tone: "success",
        text: nextPreflight.autoRecovered
          ? nextPreflight.recoveryMessage || "已自动恢复上次退出遗留的网络配置"
          : "配置与运行状态已同步",
      });
    } catch (error) {
      setNotice(noticeFromError(error));
    } finally {
      setIsBootstrapping(false);
    }
  }

  async function persistConfig(options?: { silent?: boolean; successText?: string }) {
    setIsSaving(true);
    try {
      await backend.saveConfig(config);
      setSavedConfigKey(serializeConfig(config));
      if (!options?.silent) {
        setNotice({ tone: "success", text: options?.successText ?? "配置已保存" });
      }
      return true;
    } catch (error) {
      setNotice(noticeFromError(error));
      return false;
    } finally {
      setIsSaving(false);
    }
  }

  async function saveConfig() {
    const saved = await persistConfig({ successText: "配置已保存" });
    if (saved) {
      await refreshPreflight();
    }
  }

  async function validateRules() {
    setIsValidating(true);
    try {
      const result = await backend.validateRules(config.rules);
      setValidation(result);
      setValidatedRulesKey(config.rules.join("\n"));
      if (result.summary.invalid > 0) {
        setNotice({ tone: "warning", text: `发现 ${result.summary.invalid} 条无效规则` });
      } else {
        setNotice({ tone: "success", text: `规则校验完成，共 ${result.summary.valid} 条有效规则` });
      }
    } catch (error) {
      setNotice(noticeFromError(error));
    } finally {
      setIsValidating(false);
    }
  }

  async function startRuntime() {
    setIsStarting(true);
    try {
      if (isDirty) {
        const saved = await persistConfig({ silent: true });
        if (!saved) {
          return;
        }
      }
      const report = await refreshPreflight();
      if (!report?.canStart) {
        setNotice({
          tone: "error",
          text: firstBlockingCheck(report)?.message ?? "启动前检查未通过",
        });
        return;
      }
      const startedStatus = normalizeRuntimeStatus(await backend.start());
      setRuntimeStatus(startedStatus);
      setUiRuntimeActive(true);
      const [nextRuntimeStatus, nextHealth, nextTraffic, nextPreflight] = await Promise.all([
        backend.getRuntimeStatus(),
        backend.getHealthStatus(),
        backend.getTrafficStats(),
        backend.runPreflight(),
      ]);
      const normalizedRuntimeStatus = normalizeRuntimeStatus(nextRuntimeStatus);
      setRuntimeStatus(normalizedRuntimeStatus);
      setUiRuntimeActive(normalizedRuntimeStatus.state === "running" || normalizedRuntimeStatus.state === "starting");
      setHealth(nextHealth);
      setTraffic(nextTraffic);
      setPreflight(nextPreflight);
      setNotice({ tone: "success", text: "代理隔离已启动" });
    } catch (error) {
      setNotice(noticeFromError(error));
    } finally {
      setIsStarting(false);
    }
  }

  async function stopRuntime() {
    setIsStopping(true);
    try {
      await backend.stop();
      const [nextRuntimeStatus, nextPreflight] = await Promise.all([
        backend.getRuntimeStatus(),
        backend.runPreflight(),
      ]);
      const normalizedRuntimeStatus = normalizeRuntimeStatus(nextRuntimeStatus);
      setRuntimeStatus(normalizedRuntimeStatus);
      setUiRuntimeActive(normalizedRuntimeStatus.state === "running" || normalizedRuntimeStatus.state === "starting");
      setPreflight(nextPreflight);
      setNotice({ tone: "info", text: "代理隔离已停止" });
    } catch (error) {
      setNotice(noticeFromError(error));
    } finally {
      setIsStopping(false);
    }
  }

  async function runRouteTest() {
    setIsTestingRoute(true);
    try {
      if (isDirty) {
        const saved = await persistConfig({ silent: true });
        if (!saved) {
          return;
        }
      }
      const result = await backend.testRoute(testInput.trim());
      setTestResult(result);
      setNotice({ tone: "success", text: "路由测试完成" });
    } catch (error) {
      setNotice(noticeFromError(error));
    } finally {
      setIsTestingRoute(false);
    }
  }

  async function copyLogs() {
    if (typeof navigator === "undefined" || !navigator.clipboard) {
      setNotice({ tone: "warning", text: "当前环境不支持复制日志" });
      return;
    }

    const payload = logs
      .map((entry) => {
        const details = entry.fields ? ` ${JSON.stringify(entry.fields)}` : "";
        return `${formatTimestamp(entry.timestamp)} [${entry.level || "INFO"}] ${entry.module || "app"} ${entry.message}${details}`;
      })
      .join("\n");

    try {
      await navigator.clipboard.writeText(payload);
      setNotice({ tone: "success", text: "最近日志已复制" });
    } catch (error) {
      setNotice(noticeFromError(error));
    }
  }

  async function refreshPreflight() {
    try {
      const report = await backend.runPreflight();
      setPreflight(report);
      return report;
    } catch (error) {
      setPreflight(null);
      setNotice(noticeFromError(error));
      return null;
    }
  }

  async function recoverNetwork() {
    setIsRecovering(true);
    try {
      await backend.recoverNetwork();
      const [nextRuntimeStatus, nextHealth, nextTraffic, nextPreflight] = await Promise.all([
        backend.getRuntimeStatus(),
        backend.getHealthStatus(),
        backend.getTrafficStats(),
        backend.runPreflight(),
      ]);
      const normalizedRuntimeStatus = normalizeRuntimeStatus(nextRuntimeStatus);
      setRuntimeStatus(normalizedRuntimeStatus);
      setUiRuntimeActive(normalizedRuntimeStatus.state === "running" || normalizedRuntimeStatus.state === "starting");
      setHealth(nextHealth);
      setTraffic(nextTraffic);
      setPreflight(nextPreflight);
      setNotice({ tone: "success", text: "系统网络状态已恢复" });
    } catch (error) {
      setNotice(noticeFromError(error));
    } finally {
      setIsRecovering(false);
    }
  }

  function updatePersonalPort(value: string) {
    setConfig({
      ...config,
      personalUpstream: {
        ...config.personalUpstream,
        port: sanitizePort(value),
      },
    });
  }

  function updateRules(value: string) {
    setConfig({
      ...config,
      rules: splitLines(value),
    });
  }

  function updateAdvanced<K extends keyof Config["advanced"]>(key: K, value: Config["advanced"][K]) {
    setConfig({
      ...config,
      advanced: {
        ...config.advanced,
        [key]: value,
      },
    });
  }

  function applyRuleExample() {
    setConfig({
      ...config,
      rules: sampleRules,
    });
    setNotice({ tone: "info", text: "已填充示例规则" });
  }

  function clearRules() {
    setConfig({
      ...config,
      rules: [],
    });
    setValidation(null);
    setValidatedRulesKey("");
    setNotice({ tone: "warning", text: "规则列表已清空" });
  }

  function scrollToLogs() {
    logSectionRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  const isBusy = isBootstrapping || isSaving || isStarting || isStopping || isValidating;
  const isDirty = serializeConfig(config) !== savedConfigKey;
  const configuredMode = config.advanced.personalTUNMode ? "system" : config.advanced.tunEnabled ? "tun" : config.advanced.mode || "system";
  const runtimeActive = runtimeStatus.state === "running" || runtimeStatus.state === "starting";
  const displayRuntimeActive = runtimeActive || uiRuntimeActive;
  const runtimeTransitioning = runtimeStatus.state === "starting" || runtimeStatus.state === "stopping";
  const visibleMode = displayRuntimeActive ? runtimeStatus.mode || configuredMode : configuredMode;
  const rulesKey = config.rules.join("\n");
  const activeValidation = validatedRulesKey === rulesKey ? validation : null;
  const ruleSummary = activeValidation
    ? ruleSummaryFromValidation(activeValidation.summary)
    : summarizeRules(config.rules);
  const recentLogs = logs.slice(-4).reverse();
  const blockingCheck = firstBlockingCheck(preflight);
  const warningCheck = preflight?.checks.find((item) => item.status === "warn") ?? null;
  const preflightModeHint =
    preflight && preflight.effectiveMode !== preflight.requestedMode
      ? `将以 ${formatPreflightMode(preflight.effectiveMode)} 启动`
      : "";

  const effectiveNotice =
    runtimeStatus.lastErrorMessage?.trim()
      ? { tone: "error" as const, text: runtimeStatus.lastErrorMessage }
      : notice ??
        (runtimeStatus.state === "starting"
          ? { tone: "info" as const, text: "正在启动代理隔离" }
          : runtimeStatus.state === "stopping"
            ? { tone: "info" as const, text: "正在停止代理隔离" }
            : isDirty
              ? { tone: "warning" as const, text: "配置已修改，尚未保存" }
              : runtimeStatus.state === "running"
                ? { tone: "success" as const, text: "代理隔离运行中" }
                : { tone: "info" as const, text: "配置已同步" });

  return (
    <>
      <div className="app-shell">
        <div className="app-window">
          <header className="app-header">
            <div className="app-header__left">
              <div className="app-logo">PS</div>
              <h1>ProxySeparator</h1>
              <span className="app-header__divider" />
              <span className="mode-badge">{formatModeBadge(visibleMode)}</span>
            </div>

            <div className="app-header__right">
              <HeaderStatusChip
                label="公司出口"
                value="系统路由"
                tone="company"
              />
              <HeaderStatusChip
                label="个人代理"
                value={String(config.personalUpstream.port)}
                tone="personal"
              />
              <button className="header-action" type="button" onClick={() => setRouteTesterOpen(true)}>
                <Icon name="route" className="button-icon" />
                路由测试
              </button>
            </div>
          </header>

          <main className="app-main">
            <section className="config-panel">
              <div className="section-heading">
                <h2>代理配置</h2>
                <p>公司流量复用系统路由 / 公司 VPN，其余流量走个人代理</p>
              </div>

              <div className="port-grid">
                <article className="port-card">
                  <div className="port-card__title">
                    <span className="port-card__marker port-card__marker--company">C</span>
                    <div>
                      <h3>公司网络出口</h3>
                      <p>自动复用系统路由和现有公司 VPN，不再配置公司代理端口</p>
                    </div>
                  </div>
                  <div className="input-shell input-shell--static">
                    <div className="static-field">系统路由 / 公司 VPN</div>
                    <span className="protocol-pill">
                      {formatProtocol(health?.company?.protocol || "direct")}
                    </span>
                  </div>
                  <p className="field-hint">
                    {healthHint(health?.company, "direct")}
                  </p>
                </article>

                <article className="port-card">
                  <div className="port-card__title">
                    <span className="port-card__marker port-card__marker--personal">P</span>
                    <div>
                      <h3>个人 VPN 端口</h3>
                      <p>个人 VPN 或其他代理工具的端口</p>
                    </div>
                  </div>
                  <div className="input-shell">
                    <input
                      type="number"
                      min={1}
                      max={65535}
                      value={config.personalUpstream.port}
                      onChange={(event) => updatePersonalPort(event.target.value)}
                      disabled={isBusy}
                    />
                    <span className="protocol-pill protocol-pill--personal">
                      {formatProtocol(health?.personal?.protocol || config.personalUpstream.protocol)}
                    </span>
                  </div>
                  <p className="field-hint">
                    {healthHint(health?.personal, config.personalUpstream.protocol)}
                  </p>
                </article>
              </div>

              <section className="rules-panel">
                <div className="rules-panel__header">
                  <div>
                    <h3>公司规则</h3>
                    <p>定义哪些域名 / IP 走公司网络，命中后直接复用系统路由，其他流量走个人代理</p>
                  </div>
                  <div className="rules-actions">
                    <button type="button" className="text-button" onClick={applyRuleExample}>
                      粘贴示例
                    </button>
                    <button type="button" className="text-button" onClick={clearRules}>
                      清空
                    </button>
                    <button
                      type="button"
                      className="text-button text-button--strong"
                      onClick={validateRules}
                      disabled={isValidating}
                    >
                      {isValidating ? "校验中..." : "校验规则"}
                    </button>
                  </div>
                </div>

                <textarea
                  rows={10}
                  value={config.rules.join("\n")}
                  onChange={(event) => updateRules(event.target.value)}
                  placeholder="DOMAIN-SUFFIX,company.com&#10;DOMAIN,jira.mycompany.io&#10;IP-CIDR,10.0.0.0/8"
                  disabled={isBusy}
                />

                {activeValidation?.invalidRules.length ? (
                  <div className="validation-list">
                    {activeValidation.invalidRules.map((item) => (
                      <div className="validation-item" key={`${item.line}-${item.input}`}>
                        <strong>第 {item.line} 行</strong>
                        <span>{item.reason}</span>
                        <code>{item.input}</code>
                      </div>
                    ))}
                  </div>
                ) : null}

                <div className="rules-summary">
                  <span>共 {ruleSummary.total} 条规则</span>
                  <span>域名后缀 {ruleSummary.domainSuffix}</span>
                  <span>完整域名 {ruleSummary.domainExact}</span>
                  <span>关键词 {ruleSummary.domainKeyword}</span>
                  <span>CIDR {ruleSummary.cidr}</span>
                </div>
              </section>
            </section>

            <aside className="status-panel">
              <div className="status-panel__header">
                <h2>运行状态</h2>
                <Icon name="activity" className="panel-icon" />
              </div>

              {preflight ? (
                <section className={`preflight-card ${preflight.canStart ? "is-ready" : "is-blocked"}`}>
                  <div className="preflight-card__row">
                    <span>请求模式</span>
                    <strong>{formatPreflightMode(preflight.requestedMode)}</strong>
                  </div>
                  <div className="preflight-card__row">
                    <span>实际模式</span>
                    <strong>{formatPreflightMode(preflight.effectiveMode)}</strong>
                  </div>
                  <p className="preflight-card__reason">{preflight.modeReason}</p>
                  {preflight.autoRecovered && preflight.recoveryMessage ? (
                    <p className="preflight-card__info">{preflight.recoveryMessage}</p>
                  ) : null}
                  {blockingCheck ? (
                    <p className="preflight-card__warning">{blockingCheck.message}</p>
                  ) : warningCheck ? (
                    <p className="preflight-card__info">{warningCheck.message}</p>
                  ) : null}
                  {preflight.recoveryRequired ? (
                    <button type="button" className="btn btn-outline preflight-card__button" onClick={recoverNetwork} disabled={isRecovering}>
                      {isRecovering ? "修复中..." : "修复网络状态"}
                    </button>
                  ) : null}
                </section>
              ) : null}

              <TrafficCard
                accent="company"
                title="公司流量"
                health={health?.company}
                runtimeState={runtimeStatus.state}
                totalLabel="累计流量"
                totalValue={formatBytes(traffic?.companyBytes ?? 0)}
                rateLabel="实时速率"
                rateValue={formatRate(traffic?.companyBytesPerSecond ?? 0)}
              />

              <TrafficCard
                accent="personal"
                title="个人流量"
                health={health?.personal}
                runtimeState={runtimeStatus.state}
                totalLabel="累计流量"
                totalValue={formatBytes(traffic?.personalBytes ?? 0)}
                rateLabel="实时速率"
                rateValue={formatRate(traffic?.personalBytesPerSecond ?? 0)}
              />

              <div className="stat-grid">
                <article className="mini-stat">
                  <span>当前模式</span>
                  <strong>{formatModeLabel(visibleMode)}</strong>
                </article>
                <article className="mini-stat">
                  <span>运行时长</span>
                  <strong>{formatDuration(runtimeStatus.uptimeSeconds)}</strong>
                </article>
                <article className="mini-stat">
                  <span>活动连接</span>
                  <strong>{traffic?.activeSessions ?? 0}</strong>
                </article>
              </div>

              <div className="status-overview">
                <span className={`state-pill state-pill--${runtimeStatus.state}`}>
                  {formatRuntimeState(runtimeStatus.state)}
                </span>
                <span className="status-overview__meta">
                  累计连接 {traffic?.totalSessions ?? 0} · 最近刷新 {formatTimestamp(health?.checkedAt)}
                </span>
              </div>

              <section className="logs-panel" ref={logSectionRef}>
                <div className="logs-panel__header">
                  <h3>最近日志</h3>
                  <button type="button" className="text-button text-button--strong" onClick={copyLogs}>
                    <Icon name="copy" className="inline-icon" />
                    复制
                  </button>
                </div>

                {recentLogs.length ? (
                  <div className="log-list">
                    {recentLogs.map((entry, index) => (
                      <div
                        className={`log-item log-item--${(entry.level || "INFO").toLowerCase()}`}
                        key={`${entry.timestamp || entry.message}-${index}`}
                      >
                        <span className="log-item__time">{formatTimestamp(entry.timestamp)}</span>
                        <div className="log-item__body">
                          <strong>{entry.module || "app"}</strong>
                          <span>{entry.message}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="logs-empty">运行后这里会显示最近命中的规则和运行日志</div>
                )}
              </section>
            </aside>
          </main>

          <section className={`advanced-panel ${advancedExpanded ? "is-open" : ""}`}>
            <button
              type="button"
              className="advanced-panel__header"
              onClick={() => setAdvancedExpanded((current) => !current)}
            >
              <div className="advanced-panel__title">
                <Icon name="settings" className="panel-icon" />
                <span>高级设置</span>
              </div>
              {advancedExpanded ? (
                <Icon name="chevronUp" className="panel-icon panel-icon--muted" />
              ) : (
                <Icon name="chevronDown" className="panel-icon panel-icon--muted" />
              )}
            </button>

            {advancedExpanded ? (
              <div className="advanced-grid">
                <SettingCard
                  title="启用 TUN 模式"
                  description={config.advanced.personalTUNMode ? "个人代理长期 TUN 开启时，不再使用应用内 TUN" : "适用于不走系统代理的应用"}
                  checked={config.advanced.tunEnabled}
                  disabled={config.advanced.personalTUNMode}
                  onToggle={() => {
                    const nextValue = !config.advanced.tunEnabled;
                    setConfig({
                      ...config,
                      advanced: {
                        ...config.advanced,
                        tunEnabled: nextValue,
                        mode: nextValue ? "tun" : "system",
                        udpForwarding: nextValue ? config.advanced.udpForwarding : false,
                      },
                    });
                  }}
                />
                <SettingCard
                  title="个人代理长期 TUN"
                  description="适用于 Clash / sing-box 等长期启用 TUN 的个人代理环境"
                  checked={config.advanced.personalTUNMode}
                  onToggle={() => {
                    const nextValue = !config.advanced.personalTUNMode;
                    setConfig({
                      ...config,
                      rules: nextValue ? ensureBypassCIDRs(config.rules) : config.rules,
                      advanced: {
                        ...config.advanced,
                        personalTUNMode: nextValue,
                        tunEnabled: nextValue ? false : config.advanced.tunEnabled,
                        mode: nextValue ? "system" : config.advanced.mode,
                        udpForwarding: nextValue ? false : config.advanced.udpForwarding,
                      },
                    });
                  }}
                />
                <SettingCard
                  title="启用 UDP 转发"
                  description="需要 TUN 模式支持"
                  checked={config.advanced.udpForwarding}
                  disabled={!config.advanced.tunEnabled}
                  onToggle={() => updateAdvanced("udpForwarding", !config.advanced.udpForwarding)}
                />
                <SettingCard
                  title="绕过大陆 IP"
                  description="国内流量直连不走代理"
                  checked={config.advanced.bypassChinaIP}
                  onToggle={() => updateAdvanced("bypassChinaIP", !config.advanced.bypassChinaIP)}
                />
                <SettingCard
                  title="开机自启"
                  description="系统启动时自动运行"
                  checked={config.advanced.autoStart}
                  onToggle={() => updateAdvanced("autoStart", !config.advanced.autoStart)}
                />
              </div>
            ) : null}
          </section>

          <footer className="action-bar">
            <div className={`action-status action-status--${effectiveNotice.tone}`}>
              <span className="action-status__dot" />
              <span>{preflight?.recoveryRequired ? "检测到未恢复的网络状态，请先修复" : effectiveNotice.text}</span>
            </div>

            <div className="action-bar__buttons">
              <button type="button" className="btn btn-secondary" onClick={scrollToLogs}>
                <Icon name="logs" className="button-icon" />
                查看日志
              </button>
              <button type="button" className="btn btn-secondary" onClick={() => setRouteTesterOpen(true)}>
                <Icon name="route" className="button-icon" />
                路由测试
              </button>
              <button
                type="button"
                className={`btn ${displayRuntimeActive ? "btn-danger" : "btn-primary"}`}
                onClick={displayRuntimeActive ? stopRuntime : startRuntime}
                disabled={runtimeTransitioning || isBootstrapping || isRecovering}
              >
                <Icon name={displayRuntimeActive ? "stop" : "play"} className="button-icon" />
                {displayRuntimeActive
                  ? isStopping
                    ? "停止中..."
                    : "停止隔离"
                  : isStarting
                    ? "启动中..."
                    : "启动隔离"}
              </button>
              {preflightModeHint ? <span className="action-bar__hint">{preflightModeHint}</span> : null}
              <button type="button" className="btn btn-outline" onClick={saveConfig} disabled={isSaving}>
                <Icon name="save" className="button-icon" />
                {isSaving ? "保存中..." : "保存配置"}
              </button>
            </div>
          </footer>
        </div>
      </div>

      {routeTesterOpen ? (
        <div className="modal-backdrop" onClick={() => setRouteTesterOpen(false)}>
          <div className="modal-card" onClick={(event) => event.stopPropagation()}>
            <div className="modal-card__header">
              <div>
                <h2>路由测试</h2>
                <p>输入域名或 IP，确认当前规则会把请求分配到哪个出口。</p>
              </div>
              <button type="button" className="text-button" onClick={() => setRouteTesterOpen(false)}>
                关闭
              </button>
            </div>

            <div className="route-tester">
              <input
                value={testInput}
                onChange={(event) => setTestInput(event.target.value)}
                placeholder="git.company.com"
              />
              <button type="button" className="btn btn-secondary" onClick={runRouteTest} disabled={isTestingRoute}>
                <Icon name="route" className="button-icon" />
                {isTestingRoute ? "测试中..." : "开始测试"}
              </button>
            </div>

            {testResult ? (
              <div className="route-result">
                <div className="route-result__row">
                  <span>目标出口</span>
                  <strong>{formatRouteTarget(testResult.target)}</strong>
                </div>
                <div className="route-result__row">
                  <span>规则类型</span>
                  <strong>{formatRuleType(testResult.ruleType)}</strong>
                </div>
                <div className="route-result__row">
                  <span>归一化结果</span>
                  <strong>{testResult.normalized || "--"}</strong>
                </div>
                <div className="route-result__row">
                  <span>命中规则</span>
                  <strong>{testResult.matchedRule || "未命中，走默认出口"}</strong>
                </div>
                {testResult.reason ? (
                  <div className="route-result__reason">{testResult.reason}</div>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>
      ) : null}
    </>
  );
}
