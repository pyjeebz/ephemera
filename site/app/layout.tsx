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

export const metadata: Metadata = {
  title: "ephemera: sandboxes that empty themselves",
  description:
    "Run a fleet of agents and test suites side by side, each in a throwaway microVM that can’t touch your laptop, your secrets or anything you didn’t allow.",
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
