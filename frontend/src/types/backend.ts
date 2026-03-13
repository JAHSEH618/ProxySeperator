export type UpstreamConfig = {
  host: string;
  port: number;
  protocol: string;
};

export type AdvancedConfig = {
  mode: string;
  tunEnabled: boolean;
  personalTUNMode: boolean;
  udpForwarding: boolean;
  bypassChinaIP: boolean;
  autoStart: boolean;
  startMinimized: boolean;
};

export type UIConfig = {
  language: string;
  theme: string;
};

export type Config = {
  version: number;
  companyUpstream: UpstreamConfig;
  personalUpstream: UpstreamConfig;
  rules: string[];
  advanced: AdvancedConfig;
  ui: UIConfig;
};

export type RuntimeStatus = {
  state: string;
  mode: string;
  requestedMode?: string;
  modeReason?: string;
  recoveryRequired?: boolean;
  startedAt?: string;
  uptimeSeconds: number;
  lastErrorCode?: string;
  lastErrorMessage?: string;
};

export type UpstreamHealth = {
  reachable: boolean;
  protocol: string;
  rttMs: number;
  lastSuccessAt?: string;
  consecutiveFailures: number;
};

export type HealthStatus = {
  checkedAt?: string;
  company: UpstreamHealth;
  personal: UpstreamHealth;
};

export type TrafficStats = {
  mode: string;
  startedAt?: string;
  activeSessions: number;
  totalSessions: number;
  rxBytes: number;
  txBytes: number;
  companyBytes: number;
  personalBytes: number;
  rxBytesPerSecond: number;
  txBytesPerSecond: number;
  companyBytesPerSecond: number;
  personalBytesPerSecond: number;
};

export type RouteTestResult = {
  input: string;
  normalized: string;
  target: string;
  ruleType: string;
  matchedRule?: string;
  reason?: string;
};

export type PreflightCheck = {
  id: string;
  status: "pass" | "warn" | "fail";
  code?: string;
  message: string;
};

export type PreflightReport = {
  requestedMode: string;
  effectiveMode: string;
  modeReason: string;
  canStart: boolean;
  recoveryRequired: boolean;
  autoRecovered?: boolean;
  recoveryMessage?: string;
  checks: PreflightCheck[];
};

export type InvalidRule = {
  line: number;
  input: string;
  reason: string;
};

export type RuleSummary = {
  total: number;
  valid: number;
  invalid: number;
  domainSuffix: number;
  domainExact: number;
  domainKeyword: number;
  cidr: number;
};

export type RuleValidationResult = {
  validRules: string[];
  invalidRules: InvalidRule[];
  summary: RuleSummary;
};

export type LogEntry = {
  timestamp: string;
  level: string;
  module: string;
  message: string;
  fields?: Record<string, unknown>;
};

export type ConnectionRecord = {
  id: number;
  destination: string;
  target: string;
  ruleType: string;
  matchedRule?: string;
  connectedAt: string;
};

