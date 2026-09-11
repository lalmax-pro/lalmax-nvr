"use client";

import { useEffect } from "react";

const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

declare global {
  interface Window {
    Scalar?: {
      createApiReference: (selector: string, opts: Record<string, unknown>) => void;
    };
  }
}

export default function ApiDocsPage() {
  useEffect(() => {
    const existing = document.querySelector("script[data-scalar]");
    const start = () => {
      window.Scalar?.createApiReference("#scalar-app", {
        url: `${basePath}/openapi.yaml`,
        hideClientButton: false,
        defaultHttpClient: { targetKey: "shell", clientKey: "curl" },
      });
    };
    if (existing && window.Scalar) {
      start();
      return;
    }
    const script = document.createElement("script");
    script.src = "https://cdn.jsdelivr.net/npm/@scalar/api-reference";
    script.dataset.scalar = "1";
    script.onload = start;
    document.body.appendChild(script);
  }, []);

  return (
    <div className="docs-shell">
      <header className="docs-bar wrap">
        <a className="brand" href={`${basePath}/`}>
          <span className="brand-mark">
            <i />
            <i />
            <i />
          </span>
          <span>
            lalmax<span className="brand-accent">-nvr</span>
          </span>
        </a>
        <nav className="docs-bar-links">
          <a href={`${basePath}/`}>产品官网</a>
          <a href="https://github.com/lalmax-pro/lalmax-nvr" target="_blank" rel="noreferrer">
            GitHub
          </a>
        </nav>
      </header>
      <div id="scalar-app" className="docs-scalar" />
    </div>
  );
}
