import type { Metadata } from "next";
import "./globals.css";

const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";
const siteUrl = process.env.NEXT_PUBLIC_SITE_URL;
const metadataBase = siteUrl ? new URL(new URL(siteUrl).origin) : undefined;

export const metadata: Metadata = {
  metadataBase,
  title: "lalmax-nvr · 开源跨平台网络视频录像机（NVR）",
  description:
    "开源、跨平台的 Go 网络视频录像机。统一接入 RTSP / ONVIF / GB28181 / SIP VoIP 与编码器推流，提供多协议实时播放、计划录像、连续回放、AI 事件与 Home Assistant 集成。",
  icons: { icon: `${basePath}/logo.png`, shortcut: `${basePath}/logo.png` },
  openGraph: {
    title: "lalmax-nvr · 开源跨平台网络视频录像机",
    description:
      "统一设备与流管理、多协议实时播放、计划录像、GB28181、AI 事件和 Home Assistant 集成，并提供完整技术知识库。",
    type: "website",
    locale: "zh_CN",
    images: [{ url: `${basePath}/og.png`, width: 1730, height: 909, alt: "lalmax-nvr 跨平台网络视频录像机" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "lalmax-nvr · 开源跨平台网络视频录像机",
    description:
      "开源跨平台 Go NVR：多源接入、多协议播放、计划录像、AI 事件与 Home Assistant 集成。",
    images: [`${basePath}/og.png`],
  },
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="zh-CN"><body>{children}</body></html>;
}
