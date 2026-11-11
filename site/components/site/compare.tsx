import type { ReactNode } from "react"

import { Mayfly } from "./icons"

// [anchor word, detail]. The anchor leads every cell so the table still scans
// if the detail is dropped for space.
type Cell = [string, ReactNode?]

const ROWS: [string, Cell, Cell, Cell][] = [
  ["Runs on its own kernel", ["No", "it shares yours"], ["Yes"], ["Yes", "a Firecracker microVM"]],
  ["Ready in under a second", ["Yes"], ["No", "minutes to start"], ["Yes", "about 65 ms from a snapshot"]],
  ["Network allowed per host, blocked by default", ["No", "all or nothing"], ["No"], ["Yes", "with every attempt logged"]],
  ["Deleted when the command ends", ["Only with", <code key="rm">--rm</code>], ["No", "it stays until you stop it"], ["Always", "disk included"]],
  ["Signed record of what ran", ["No"], ["No"], ["Yes", <>checked with <code>ephemera verify</code></>]],
  ["Runs on hardware you own", ["Yes"], ["No"], ["Yes", "self-hosted"]],
  // The one ephemera loses. It stays last and untinted, so it reads as plainly as the rest.
  ["Works without a server", ["Yes"], ["No"], ["No", "it needs a Linux host with KVM"]],
]

function Answer({ cell, us }: { cell: Cell; us?: boolean }) {
  const [word, detail] = cell
  return (
    <td className={us ? "us" : undefined}>
      <span className={word === "No" ? "ans neg" : "ans"}>{word}</span>
      {detail && (
        <span className="detail">
          {word === "Only with" ? " " : ", "}
          {detail}
        </span>
      )}
    </td>
  )
}

export function Compare() {
  return (
    <section className="section compare-section">
      <h2>Why not just a container, or a cloud dev box?</h2>
      <div className="table-wrap">
        <table className="compare">
          <thead>
            <tr>
              <td />
              <th scope="col">Docker on your laptop</th>
              <th scope="col">Cloud dev box</th>
              <th scope="col" className="us">
                <span className="us-mark">
                  <Mayfly width={28} />
                </span>
                ephemera
              </th>
            </tr>
          </thead>
          <tbody>
            {ROWS.map(([claim, docker, cloud, us], i) => (
              <tr key={claim} className={i === ROWS.length - 1 ? "honest" : undefined}>
                <th scope="row">{claim}</th>
                <Answer cell={docker} />
                <Answer cell={cloud} />
                <Answer cell={us} us />
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}
