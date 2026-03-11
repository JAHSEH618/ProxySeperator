import React from "react";
import ReactDOM from "react-dom/client";

import "./runtime-loader";
import { App } from "./App";
import "./styles.css";

type FatalBoundaryState = {
  error: Error | null;
};

class FatalErrorBoundary extends React.Component<React.PropsWithChildren, FatalBoundaryState> {
  state: FatalBoundaryState = {
    error: null,
  };

  static getDerivedStateFromError(error: Error): FatalBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error) {
    console.error("Frontend bootstrap failed", error);
  }

  render() {
    if (!this.state.error) {
      return this.props.children;
    }

    return (
      <div
        style={{
          minHeight: "100vh",
          display: "grid",
          placeItems: "center",
          padding: "32px",
          background: "#eef3f4",
          color: "#10212b",
        }}
      >
        <div
          style={{
            width: "min(720px, 100%)",
            padding: "24px",
            borderRadius: "20px",
            border: "1px solid #d7e0e4",
            background: "#ffffff",
            boxShadow: "0 18px 44px rgba(16, 33, 43, 0.08)",
          }}
        >
          <h1 style={{ margin: 0, fontSize: "24px" }}>前端启动失败</h1>
          <p style={{ margin: "12px 0 0", lineHeight: 1.6 }}>
            {this.state.error.message}
          </p>
          <pre
            style={{
              margin: "16px 0 0",
              padding: "16px",
              overflow: "auto",
              borderRadius: "14px",
              background: "#edf2f4",
              whiteSpace: "pre-wrap",
            }}
          >
            {this.state.error.stack}
          </pre>
        </div>
      </div>
    );
  }
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <FatalErrorBoundary>
      <App />
    </FatalErrorBoundary>
  </React.StrictMode>,
);
