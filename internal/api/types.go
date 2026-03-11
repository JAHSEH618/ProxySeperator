package api

import "time"

const (
	AppName = "ProxySeparator"
)

const (
	ModeSystem = "system"
	ModeTUN    = "tun"
)

const (
	RuntimeStateIdle     = "idle"
	RuntimeStateStarting = "starting"
	RuntimeStateRunning  = "running"
	RuntimeStateStopping = "stopping"
	RuntimeStateError    = "error"
)

const (
	ProtocolAuto    = "auto"
	ProtocolHTTP    = "http"
	ProtocolSOCKS5  = "socks5"
	ProtocolUnknown = "unknown"
)

const (
	RouteTargetCompany  = "company"
	RouteTargetPersonal = "personal"
	RouteTargetDirect   = "direct"
)

const (
	RuleTypeDomainSuffix  = "DOMAIN_SUFFIX"
	RuleTypeDomainExact   = "DOMAIN_EXACT"
	RuleTypeDomainKeyword = "DOMAIN_KEYWORD"
	RuleTypeIPCIDR        = "IP_CIDR"
	RuleTypeLocalIP       = "LOCAL_IP"
	RuleTypeDefault       = "DEFAULT"
)

const (
	EventRuntimeStatus  = "runtime:status"
	EventRuntimeHealth  = "runtime:health"
	EventRuntimeTraffic = "runtime:traffic"
	EventRuntimeError   = "runtime:error"
	EventRuntimeLog     = "runtime:log"
)

type UpstreamConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

func (u UpstreamConfig) Address() string {
	return u.Host + ":" + itoa(u.Port)
}

type AdvancedConfig struct {
	Mode           string `json:"mode"`
	TUNEnabled     bool   `json:"tunEnabled"`
	UDPForwarding  bool   `json:"udpForwarding"`
	BypassChinaIP  bool   `json:"bypassChinaIP"`
	AutoStart      bool   `json:"autoStart"`
	StartMinimized bool   `json:"startMinimized"`
}

type UIConfig struct {
	Language string `json:"language"`
	Theme    string `json:"theme"`
}

type Config struct {
	Version          int            `json:"version"`
	CompanyUpstream  UpstreamConfig `json:"companyUpstream"`
	PersonalUpstream UpstreamConfig `json:"personalUpstream"`
	Rules            []string       `json:"rules"`
	Advanced         AdvancedConfig `json:"advanced"`
	UI               UIConfig       `json:"ui"`
}

func DefaultConfig() Config {
	return Config{
		Version: 1,
		CompanyUpstream: UpstreamConfig{
			Host:     "127.0.0.1",
			Port:     7890,
			Protocol: ProtocolAuto,
		},
		PersonalUpstream: UpstreamConfig{
			Host:     "127.0.0.1",
			Port:     7897,
			Protocol: ProtocolAuto,
		},
		Rules: []string{
			".company.com",
			".internal",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
		},
		Advanced: AdvancedConfig{
			Mode:           ModeSystem,
			TUNEnabled:     false,
			UDPForwarding:  false,
			BypassChinaIP:  false,
			AutoStart:      false,
			StartMinimized: false,
		},
		UI: UIConfig{
			Language: "zh-CN",
			Theme:    "system",
		},
	}
}

type RuntimeStatus struct {
	State            string     `json:"state"`
	Mode             string     `json:"mode"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	UptimeSeconds    int64      `json:"uptimeSeconds"`
	LastErrorCode    string     `json:"lastErrorCode,omitempty"`
	LastErrorMessage string     `json:"lastErrorMessage,omitempty"`
}

type UpstreamHealth struct {
	Reachable           bool      `json:"reachable"`
	Protocol            string    `json:"protocol"`
	RTTMs               int64     `json:"rttMs"`
	LastSuccessAt       time.Time `json:"lastSuccessAt,omitempty"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
}

type HealthStatus struct {
	CheckedAt time.Time      `json:"checkedAt"`
	Company   UpstreamHealth `json:"company"`
	Personal  UpstreamHealth `json:"personal"`
}

type TrafficStats struct {
	Mode                   string     `json:"mode"`
	StartedAt              *time.Time `json:"startedAt,omitempty"`
	ActiveSessions         int64      `json:"activeSessions"`
	TotalSessions          int64      `json:"totalSessions"`
	RXBytes                uint64     `json:"rxBytes"`
	TXBytes                uint64     `json:"txBytes"`
	CompanyBytes           uint64     `json:"companyBytes"`
	PersonalBytes          uint64     `json:"personalBytes"`
	RXBytesPerSecond       float64    `json:"rxBytesPerSecond"`
	TXBytesPerSecond       float64    `json:"txBytesPerSecond"`
	CompanyBytesPerSecond  float64    `json:"companyBytesPerSecond"`
	PersonalBytesPerSecond float64    `json:"personalBytesPerSecond"`
}

type RouteTestResult struct {
	Input       string `json:"input"`
	Normalized  string `json:"normalized"`
	Target      string `json:"target"`
	RuleType    string `json:"ruleType"`
	MatchedRule string `json:"matchedRule,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type InvalidRule struct {
	Line   int    `json:"line"`
	Input  string `json:"input"`
	Reason string `json:"reason"`
}

type RuleSummary struct {
	Total         int `json:"total"`
	Valid         int `json:"valid"`
	Invalid       int `json:"invalid"`
	DomainSuffix  int `json:"domainSuffix"`
	DomainExact   int `json:"domainExact"`
	DomainKeyword int `json:"domainKeyword"`
	CIDR          int `json:"cidr"`
}

type RuleValidationResult struct {
	ValidRules   []string      `json:"validRules"`
	InvalidRules []InvalidRule `json:"invalidRules"`
	Summary      RuleSummary   `json:"summary"`
}

type LogEntry struct {
	Timestamp time.Time      `json:"timestamp"`
	Level     string         `json:"level"`
	Module    string         `json:"module"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return sign + string(buf[i:])
}
