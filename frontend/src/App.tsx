import { useEffect, useState } from "react";

import { backend, runtimeEvents } from "./lib/backend";
import type {
  Config,
  HealthStatus,
  LogEntry,
  RouteTestResult,
  RuleValidationResult,
  RuntimeStatus,
  TrafficStats,
} from "./types/backend";

const defaultConfig: Config = {
  version: 1,
  companyUpstream: { host: "127.0.0.1", port: 7890, protocol: "auto" },
  personalUpstream: { host: "127.0.0.1", port: 7897, protocol: "auto" },
  rules: [".company.com", ".internal", "10.0.0.0/8"],
  advanced: {
    mode: "system",
    tunEnabled: false,
    udpForwarding: false,
    bypassChinaIP: false,
    autoStart: false,
    startMinimized: false,
  },
  ui: { language: "zh-CN", theme: "system" },
};

export function App() {
  const [config, setConfig] = useState<Config>(defaultConfig);
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
  const [message, setMessage] = useState("");

  useEffect(() => {
    const disposers = [
      runtimeEvents.onRuntimeStatus(setRuntimeStatus),
      runtimeEvents.onRuntimeHealth(setHealth),
      runtimeEvents.onRuntimeTraffic(setTraffic),
      runtimeEvents.onRuntimeLog((entry) => setLogs((current) => [...current.slice(-49), entry])),
    ];

    void bootstrap();
    return () => {
      disposers.forEach((dispose) => dispose());
    };
  }, []);

  async function bootstrap() {
    try {
      const loadedConfig = await backend.loadConfig();
      setConfig(loadedConfig);
      setRuntimeStatus(await backend.getRuntimeStatus());
      setHealth(await backend.getHealthStatus());
      setTraffic(await backend.getTrafficStats());
      setLogs(await backend.listLogs(20));
      setMessage("配置与运行状态已加载");
    } catch (error) {
      setMessage(String(error));
    }
  }

  async function saveConfig() {
    try {
      await backend.saveConfig(config);
      setMessage("配置已保存");
    } catch (error) {
      setMessage(String(error));
    }
  }

  async function validateRules() {
    try {
      const result = await backend.validateRules(config.rules);
      setValidation(result);
      setMessage(`规则校验完成，合法 ${result.summary.valid} 条`);
    } catch (error) {
      setMessage(String(error));
    }
  }

  async function startRuntime() {
    try {
      const status = await backend.start();
      setRuntimeStatus(status);
      setMessage("已请求启动隔离");
    } catch (error) {
      setMessage(String(error));
    }
  }

  async function stopRuntime() {
    try {
      await backend.stop();
      setMessage("已请求停止隔离");
      setRuntimeStatus(await backend.getRuntimeStatus());
    } catch (error) {
      setMessage(String(error));
    }
  }

  async function runRouteTest() {
    try {
      const result = await backend.testRoute(testInput);
      setTestResult(result);
      setMessage("规则测试完成");
    } catch (error) {
      setMessage(String(error));
    }
  }

  return (
    <div className="app-shell">
      <header className="hero">
        <div>
          <p className="eyebrow">ProxySeparator / Wails MVP Shell</p>
          <h1>后端接口联调面板</h1>
          <p className="subtitle">当前交付的是可启动壳与调试界面，供前端后续接完整 UI。</p>
        </div>
        <div className="status-badge">
          <strong>{runtimeStatus.state}</strong>
          <span>{runtimeStatus.mode}</span>
        </div>
      </header>

      <section className="grid">
        <article className="panel">
          <h2>配置</h2>
          <label>
            公司代理端口
            <input
              type="number"
              value={config.companyUpstream.port}
              onChange={(event) =>
                setConfig({
                  ...config,
                  companyUpstream: {
                    ...config.companyUpstream,
                    port: Number(event.target.value),
                  },
                })
              }
            />
          </label>
          <label>
            个人代理端口
            <input
              type="number"
              value={config.personalUpstream.port}
              onChange={(event) =>
                setConfig({
                  ...config,
                  personalUpstream: {
                    ...config.personalUpstream,
                    port: Number(event.target.value),
                  },
                })
              }
            />
          </label>
          <label>
            公司规则
            <textarea
              rows={10}
              value={config.rules.join("\n")}
              onChange={(event) =>
                setConfig({
                  ...config,
                  rules: event.target.value.split("\n"),
                })
              }
            />
          </label>
          <div className="actions">
            <button onClick={saveConfig}>保存配置</button>
            <button onClick={validateRules}>校验规则</button>
            <button onClick={startRuntime}>启动隔离</button>
            <button onClick={stopRuntime}>停止隔离</button>
          </div>
          {validation ? (
            <pre className="json-block">{JSON.stringify(validation, null, 2)}</pre>
          ) : null}
        </article>

        <article className="panel">
          <h2>运行状态</h2>
          <pre className="json-block">{JSON.stringify(runtimeStatus, null, 2)}</pre>
          <h3>健康检查</h3>
          <pre className="json-block">{JSON.stringify(health, null, 2)}</pre>
          <h3>流量统计</h3>
          <pre className="json-block">{JSON.stringify(traffic, null, 2)}</pre>
        </article>

        <article className="panel">
          <h2>规则测试器</h2>
          <label>
            域名 / IP
            <input value={testInput} onChange={(event) => setTestInput(event.target.value)} />
          </label>
          <div className="actions">
            <button onClick={runRouteTest}>测试路由</button>
          </div>
          {testResult ? <pre className="json-block">{JSON.stringify(testResult, null, 2)}</pre> : null}
        </article>

        <article className="panel panel-wide">
          <h2>实时日志</h2>
          <p className="message">{message}</p>
          <pre className="json-block">{JSON.stringify(logs, null, 2)}</pre>
        </article>
      </section>
    </div>
  );
}
