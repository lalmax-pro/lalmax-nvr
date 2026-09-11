import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "lalmax-nvr API 文档",
  description: "lalmax-nvr REST API 参考。HTTP Basic Auth，默认端口 9090。",
};

export default function DocsLayout({ children }: { children: React.ReactNode }) {
  return children;
}
