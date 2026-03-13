package api

import (
	"encoding/json"
	"time"
)

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
	ProtocolDirect  = "direct"
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
	EventRuntimeStatus      = "runtime:status"
	EventRuntimeHealth      = "runtime:health"
	EventRuntimeTraffic     = "runtime:traffic"
	EventRuntimeError       = "runtime:error"
	EventRuntimeLog         = "runtime:log"
	EventRuntimeConnections = "runtime:connections"
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
	Mode            string `json:"mode"`
	TUNEnabled      bool   `json:"tunEnabled"`
	PersonalTUNMode bool   `json:"personalTUNMode"`
	UDPForwarding   bool   `json:"udpForwarding"`
	BypassChinaIP   bool   `json:"bypassChinaIP"`
	AutoStart       bool   `json:"autoStart"`
	StartMinimized  bool   `json:"startMinimized"`
	FailOpenDirect  *bool  `json:"failOpenDirect,omitempty"` // nil defaults to true
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
			Host:     "system-route",
			Port:     0,
			Protocol: ProtocolDirect,
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
			Mode:            ModeSystem,
			TUNEnabled:      false,
			PersonalTUNMode: false,
			UDPForwarding:   false,
			BypassChinaIP:   false,
			AutoStart:       false,
			StartMinimized:  false,
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
	RequestedMode    string     `json:"requestedMode,omitempty"`
	ModeReason       string     `json:"modeReason,omitempty"`
	RecoveryRequired bool       `json:"recoveryRequired,omitempty"`
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

type PreflightCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type PreflightReport struct {
	RequestedMode    string           `json:"requestedMode"`
	EffectiveMode    string           `json:"effectiveMode"`
	ModeReason       string           `json:"modeReason"`
	CanStart         bool             `json:"canStart"`
	RecoveryRequired bool             `json:"recoveryRequired"`
	AutoRecovered    bool             `json:"autoRecovered,omitempty"`
	RecoveryMessage  string           `json:"recoveryMessage,omitempty"`
	Checks           []PreflightCheck `json:"checks"`
}

type SystemProxyState struct {
	Enabled      bool   `json:"enabled"`
	HTTPAddress  string `json:"httpAddress,omitempty"`
	HTTPSAddress string `json:"httpsAddress,omitempty"`
	SOCKSAddress string `json:"socksAddress,omitempty"`
	Mixed        bool   `json:"mixed,omitempty"`
}

type TUNRecoveryState struct {
	Interface       string   `json:"interface,omitempty"`
	EgressInterface string   `json:"egressInterface,omitempty"`
	Routes          []string `json:"routes,omitempty"`
	DNSListen       string   `json:"dnsListen,omitempty"`
}

type CompanyBypassState struct {
	Interface string   `json:"interface,omitempty"`
	Routes    []string `json:"routes,omitempty"`
}

type RecoverySnapshot struct {
	Platform        string             `json:"platform"`
	Mode            string             `json:"mode"`
	WrittenAt       time.Time          `json:"writtenAt"`
	SystemProxy     SystemProxyState   `json:"systemProxy"`
	SystemProxyData json.RawMessage    `json:"systemProxyData,omitempty"`
	DNSState        json.RawMessage    `json:"dnsState,omitempty"`
	TUNState        TUNRecoveryState   `json:"tunState,omitempty"`
	CompanyBypass   CompanyBypassState `json:"companyBypass,omitempty"`
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

type ConnectionRecord struct {
	ID          int64     `json:"id"`
	Destination string    `json:"destination"`
	Target      string    `json:"target"`
	RuleType    string    `json:"ruleType"`
	MatchedRule string    `json:"matchedRule,omitempty"`
	ConnectedAt time.Time `json:"connectedAt"`
}

type RuleTemplate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Rules       []string `json:"rules"`
}

func BuiltinRuleTemplates() []RuleTemplate {
	return []RuleTemplate{
		{
			ID:          "bytedance",
			Name:        "字节跳动",
			Description: "字节跳动内网域名与常用 CIDR",
			Rules: []string{
				".bytedance.net",
				".bytedance.com",
				".byted.org",
				".feishu.cn",
				".feishu.net",
				".larksuite.com",
				".volcengine.com",
				"10.0.0.0/8",
			},
		},
		{
			ID:          "alibaba",
			Name:        "阿里巴巴",
			Description: "阿里巴巴内网域名与常用 CIDR",
			Rules: []string{
				".alibaba-inc.com",
				".alipay.net",
				".antgroup-inc.cn",
				".taobao.org",
				".aone.alibaba-inc.com",
				".aliyun-inc.com",
				"10.0.0.0/8",
				"100.64.0.0/10",
			},
		},
		{
			ID:          "tencent",
			Name:        "腾讯",
			Description: "腾讯内网域名与常用 CIDR",
			Rules: []string{
				".tencent.com",
				".woa.com",
				".oa.com",
				".weixin.qq.com",
				".km.woa.com",
				"10.0.0.0/8",
				"9.0.0.0/8",
			},
		},
		{
			ID:          "meituan",
			Name:        "美团",
			Description: "美团内网域名与常用 CIDR",
			Rules: []string{
				".meituan.com",
				".sankuai.com",
				".dianping.com",
				".mws.sankuai.com",
				"10.0.0.0/8",
			},
		},
		{
			ID:          "private-cidr",
			Name:        "RFC 1918 私有网段",
			Description: "所有标准私有 IP 地址段",
			Rules: []string{
				"10.0.0.0/8",
				"172.16.0.0/12",
				"192.168.0.0/16",
				"100.64.0.0/10",
			},
		},
	}
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
