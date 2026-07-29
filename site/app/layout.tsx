import type { Metadata } from "next";
import "./globals.css";

const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";
const siteUrl = process.env.NEXT_PUBLIC_SITE_URL;

export const metadata: Metadata = {
  metadataBase: siteUrl ? new URL(siteUrl) : undefined,
  title: "lalmax-nvr · 跨平台网络视频录像机",
  description: "以统一媒体链路连接摄像头、实时预览、智能录像与跨平台部署。",
  icons: { icon: `${basePath}/logo.png`, shortcut: `${basePath}/logo.png` },
  openGraph: {
    title: "lalmax-nvr · 跨平台网络视频录像机",
    description: "统一接入、实时预览、智能录像。",
    type: "website",
    locale: "zh_CN",
    images: [{ url: `${basePath}/og.png`, width: 1200, height: 630, alt: "lalmax-nvr 跨平台网络视频录像机" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "lalmax-nvr · 跨平台网络视频录像机",
    description: "统一接入、实时预览、智能录像。",
    images: [`${basePath}/og.png`],
  },
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="zh-CN"><body>{children}</body></html>;
}
