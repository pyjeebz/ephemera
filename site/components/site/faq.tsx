import type { ReactNode } from "react"

import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion"

const QUESTIONS: [string, ReactNode][] = [
  [
    "Do I edit code inside the sandbox?",
    "No. You and your agent keep editing on your laptop. Each run sends the files Git tracks, so the sandbox always runs what’s in your editor.",
  ],
  [
    "Can anyone see my code?",
    "Files, output and secrets go straight from your laptop to the host that runs the sandbox. The central server only places sandboxes and tracks their IDs and states, and it never sees your code. You run both, so nothing leaves machines you control.",
  ],
  [
    "What about .env.local?",
    <>
      Ignored files stay on your machine unless you pass <code>--include</code>. Files you include are added after setup, so
      they’re never saved in a setup snapshot.
    </>,
  ],
  [
    "What’s left after a run?",
    <>
      A signed record on your laptop: commands, exit codes, syncs, connections, and proof the disk was deleted. If you used{" "}
      <code>--setup</code>, the host also keeps the setup snapshot, encrypted with your key. Nothing else.
    </>,
  ],
  [
    "What do I need to run it?",
    "For the CLI, macOS, Linux or Windows. For a host, an x86_64 Linux machine with KVM; we test on Ubuntu 24.04. One machine can run both the central server and the host.",
  ],
  [
    "How long can a sandbox live?",
    <>
      <code>ephemera run</code> sandboxes end with their command. <code>ephemera dev</code> sandboxes end when you stop the
      session. Hosts destroy any sandbox after 24 hours by default, and <code>--timeout</code> stops a single command sooner.
    </>,
  ],
]

export function Faq() {
  return (
    <section className="section faq">
      <h2>FAQ</h2>
      <Accordion type="single" collapsible defaultValue="q0" className="faq-list">
        {QUESTIONS.map(([q, a], i) => (
          <AccordionItem key={q} value={`q${i}`} className="border-b border-border">
            <AccordionTrigger className="py-5 text-[17px] font-medium text-foreground hover:underline-offset-4 **:data-[slot=accordion-trigger-icon]:text-faint">
              {q}
            </AccordionTrigger>
            <AccordionContent className="max-w-[640px] pb-5 text-[15px] leading-[23px] text-muted-foreground">{a}</AccordionContent>
          </AccordionItem>
        ))}
      </Accordion>
    </section>
  )
}
