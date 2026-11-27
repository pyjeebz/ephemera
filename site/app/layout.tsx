import type { Metadata } from "next"
import { JetBrains_Mono } from "next/font/google"
import localFont from "next/font/local"

import { TooltipProvider } from "@/components/ui/tooltip"
import "./globals.css"

// Satoshi, self-hosted from Fontshare's download (ITF Free Font License).
const satoshi = localFont({
  src: "./fonts/Satoshi-Variable.woff2",
  variable: "--font-satoshi",
  weight: "300 900",
  display: "swap",
})

const mono = JetBrains_Mono({
  subsets: ["latin"],
  variable: "--font-jetbrains",
  weight: ["400", "500"],
})

const title = "ephemera: sandboxes that empty themselves"
const description =
  "Run a fleet of agents and test suites side by side, each in a throwaway microVM that can’t touch your laptop, your secrets or anything you didn’t allow."

// Link previews use app/opengraph-image.png, made with scripts/og-image.html.
export const metadata: Metadata = {
  metadataBase: new URL("https://ephemera.run"),
  title,
  description,
  openGraph: { title, description, url: "/", siteName: "ephemera", type: "website" },
  twitter: { card: "summary_large_image", title, description },
}

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={`dark ${satoshi.variable} ${mono.variable}`}>
      <body>
        <TooltipProvider delayDuration={500}>{children}</TooltipProvider>
      </body>
    </html>
  )
}
