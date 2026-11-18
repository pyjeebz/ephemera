import { Brand, type BrandIcon } from "./icons"

const CLIENTS: [string, BrandIcon][] = [
  ["Claude Code", "claude"],
  ["Codex", "openai"],
  ["Cursor", "cursor"],
  ["Gemini CLI", "googlegemini"],
]

const STACK: [string, BrandIcon][] = [
  ["Node.js 24", "nodedotjs"],
  ["npm", "npm"],
  ["Python 3", "python"],
  ["pip", "pypi"],
  ["git", "git"],
  ["gcc and make", "gnu"],
]

// The isometric drawings are built by lib/engine/iso.js into each
// svg[data-iso] once the page has mounted.
export function Features() {
  return (
    <section className="section features">
      <div className="section-head">
        <h2>Built for developers and the agents they let loose</h2>
      </div>
      <div className="fgrid">
        <div className="fcell wide">
          <div className="ftext">
            <h3>Port forwarding to localhost</h3>
            <p>
              <code>ephemera dev</code> forwards the sandbox’s ports to your laptop and syncs each file as you save it, so hot
              reload keeps working. Run several branches on one port: each gets its own address.
            </p>
          </div>
          <svg className="iso" data-iso="ports" viewBox="0 0 520 330" role="img" aria-label="A laptop forwarding localhost:3000 to one of two sandboxes, feat-auth and main." />
        </div>

        <div className="fcell">
          <div className="ftext">
            <h3>Coding agents, over MCP</h3>
            <p>
              <code>ephemera mcp</code> gives your agent a <code>run_command</code> tool. Every call runs in a fresh sandbox and
              returns the exit code, the output, and any hosts that were blocked.
            </p>
          </div>
          <div className="flist">
            <span className="label">Works with any MCP client</span>
            <ul>
              {CLIENTS.map(([name, icon]) => (
                <li key={name}>
                  <Brand name={icon} className="logo-mark" />
                  {name}
                </li>
              ))}
              <li className="dashed">any MCP client</li>
            </ul>
          </div>
        </div>

        <div className="fcell">
          <div className="ftext">
            <h3>What’s inside a sandbox</h3>
            <p>
              Each sandbox has its own disk, so <code>apt install</code> works. The disk is deleted with the sandbox.
            </p>
          </div>
          <div className="flist">
            <span className="label">
              <Brand name="ubuntu" className="logo-mark" size={14} />
              Ubuntu 24.04 with
            </span>
            <ul>
              {STACK.map(([name, icon]) => (
                <li key={name}>
                  <Brand name={icon} className="logo-mark" />
                  {name}
                </li>
              ))}
            </ul>
          </div>
        </div>

        <div className="fcell wide flip">
          <svg className="iso" data-iso="network" viewBox="0 0 520 330" role="img" aria-label="A sandbox whose connections pass through a proxy: registry.npmjs.org is let through, 169.254.169.254 and telemetry.example.com are blocked at the wall." />
          <div className="ftext">
            <h3>No network unless you allow it</h3>
            <p>
              A microVM has no network device. Its only way out is a proxy on the host that allows the hosts you pass with{" "}
              <code>--allow</code> and always blocks private addresses and cloud metadata. Every attempt is logged.
            </p>
          </div>
        </div>

        <div className="fcell full">
          <div className="ftext">
            <h3>A signed record of every sandbox</h3>
            <p>
              When a sandbox is destroyed, its host signs a record of what happened: commands and exit codes, file syncs,
              connections, and that the disk was deleted. Output and file contents are never recorded.
            </p>
            <div className="mono-list">
              <span>$ ephemera verify api-tests</span>
              <span className="dim">Verified: signed by host host-1; the key is the one the central server has for this host.</span>
            </div>
          </div>
          <svg className="iso" data-iso="records" viewBox="0 0 640 300" role="img" aria-label="Sand pours into a sandbox and drains away; a signed record rises out of it onto a stack of records." />
        </div>
      </div>
    </section>
  )
}
