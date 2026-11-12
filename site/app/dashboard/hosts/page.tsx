import type { Metadata } from "next"

import { CopyLine } from "@/components/app/copy-line"
import { Capacity, Dashboard, HostDot, PageTitle, Section } from "@/components/app/dash"
import { Button } from "@/components/ui/button"
import { API, HOSTS } from "@/lib/sample"

export const metadata: Metadata = { title: "Hosts" }

const CODE = "ej_k3v7q2mxa5t4wd6yr3bn5hc2pe"

export default function Hosts() {
  return (
    <Dashboard active="Hosts">
      <PageTitle title="Hosts." rest="Your machines, where sandboxes run." />

      <section className="mt-10 border border-sand/40 bg-card p-6">
        <div className="flex flex-wrap items-baseline justify-between gap-4">
          <h2 className="text-[15px] font-medium">Add a host</h2>
          <p className="font-mono text-xs text-faint">code works once · 14:32 left</p>
        </div>
        <p className="mt-1.5 max-w-[620px] text-[13px] leading-5 text-muted-foreground">
          On any x86_64 Linux machine with KVM. It installs the host agent and joins this account. The host connects out
          to ephemera, so it needs no open ports for us.
        </p>
        <CopyLine
          command={`curl -fsSL https://ephemera.run/host | sudo sh -s -- ${CODE}`}
          className="mt-4 bg-background"
        />
        <p className="mt-3 text-[13px] text-muted-foreground">
          Has ephemera-host already?{" "}
          <code className="font-mono whitespace-normal! text-foreground [overflow-wrap:anywhere]">
            sudo ephemera-host join --server {API} {CODE}
          </code>
        </p>
      </section>

      <Section
        title="Joined"
        aside={`${HOSTS.filter((h) => h.online).length} of ${HOSTS.length} online`}
        className="mt-12"
      >
        <ul className="border border-border">
          {HOSTS.map((h, i) => (
            <li
              key={h.id}
              className={`grid grid-cols-1 items-center gap-4 px-5 py-4 md:grid-cols-[220px_180px_1fr_auto] ${i > 0 ? "border-t border-border" : ""}`}
            >
              <div>
                <div className="flex items-center gap-2.5">
                  <HostDot host={h} />
                  <span className="font-medium">{h.name}</span>
                </div>
                <p className="mt-1 pl-4 font-mono text-xs text-faint">{h.id}</p>
              </div>
              <div className="text-[13px] text-muted-foreground">
                {h.online ? <Capacity host={h} /> : <>offline, seen {h.lastSeen}</>}
              </div>
              <div className="min-w-0">
                <p className="truncate font-mono text-xs text-muted-foreground" title={h.fingerprint}>
                  {h.fingerprint}
                </p>
                <p className="mt-1 text-xs text-faint">
                  {h.version} · joined {h.joined}
                </p>
              </div>
              <Button
                variant="ghost"
                size="sm"
                className="justify-self-start text-muted-foreground hover:text-destructive md:justify-self-end"
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
        <p className="mt-4 max-w-[680px] text-xs leading-[18px] text-faint">
          The fingerprint is the host’s key; ephemera-host join printed the same one. Removing a host destroys its
          sandboxes and it can’t sign in again until it joins with a new code.
        </p>
      </Section>
    </Dashboard>
  )
}
