import type { Metadata } from "next";
import "./globals.css";

const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";
const siteUrl = process.env.NEXT_PUBLIC_SITE_URL;

export const metadata: Metadata = {
  metadataBase: siteUrl ? new URL(siteUrl) : undefined,
  title: "lalmax-nvr · 开源跨平台网络视频录像机（NVR）",
  description:
    "单二进制、零依赖的 Go 网络视频录像机。RTSP / ONVIF / GB28181 / RTMP / SRT 统一接入，实时预览、智能录像、时间线回放、AI 检测与国标级联，一套搞定。",
  icons: { icon: `${basePath}/logo.png`, shortcut: `${basePath}/logo.png` },
  openGraph: {
    title: "lalmax-nvr · 开源跨平台网络视频录像机",
    description:
      "一个 Go 二进制连接摄像头、直播、录像、国标平台与智能事件：全协议接入、毫秒级预览、AI 检测分析、GB28181 级联。",
    type: "website",
    locale: "zh_CN",
    images: [{ url: `${basePath}/og.png`, width: 1200, height: 630, alt: "lalmax-nvr 跨平台网络视频录像机" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "lalmax-nvr · 开源跨平台网络视频录像机",
    description:
      "单二进制、零依赖的 Go NVR：全协议接入、实时预览、智能录像、AI 检测与国标级联。",
    images: [`${basePath}/og.png`],
  },
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="zh-CN"><body>{children}</body></html>;
}
