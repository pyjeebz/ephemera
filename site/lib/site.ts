export const GITHUB = "https://github.com/pyjeebz/ephemera"
export const INSTALL = "curl -fsSL https://ephemera.run/install | sh"

// The docs are the Mintlify site at ephemera.run/docs. Links stay absolute,
// as the copy prompts hand them to coding agents.
const BLOB = `${GITHUB}/blob/main`
export const DOCS_ROOT = "https://ephemera.run/docs"
export const DOCS = DOCS_ROOT
export const INSTALL_DOCS = `${DOCS_ROOT}/reference/install`
export const HOW_IT_WORKS = `${DOCS_ROOT}/how-it-works`
export const SNAPSHOTS = `${DOCS_ROOT}/guides/setup`
export const MCP = `${DOCS_ROOT}/guides/agents`
export const BENCHMARKS = `${GITHUB}#benchmarks`
export const SELF_HOST = `${DOCS_ROOT}/self-hosting`
export const SECURITY = `${DOCS_ROOT}/security`
export const CHANGELOG = `${DOCS_ROOT}/changelog`
export const LICENSE = `${BLOB}/LICENSE`

// What "copy the prompt" puts on the clipboard: enough for a coding agent to
// install the CLI and add ephemera as an MCP server, without seeing the token.
export const AGENT_PROMPT = `Set up ephemera for this project, so your shell commands run in throwaway sandboxes instead of on my machine.

1. Install the CLI: ${INSTALL}
2. Ask me for my ephemera server URL. Then tell me to run \`ephemera login --server <URL>\` myself and paste my API token there. Don't ask for the token or print it.
3. Run \`ephemera doctor\` and fix anything it reports.
4. Add ephemera as an MCP server. For Claude Code: \`claude mcp add ephemera -- ephemera mcp\`. Other agents take { "command": "ephemera", "args": ["mcp"] } in their MCP settings.

Docs: ${MCP}`

// What the dashboard's "Copy setup prompt" copies: the same as above for an
// account on ephemera.run, where signing in is a browser approval.
export const DASHBOARD_PROMPT = `Set up ephemera for this project, so your shell commands run in throwaway sandboxes instead of on my machine.

1. Install the CLI: ${INSTALL}
2. Run \`ephemera login\`. It prints a code and opens a page; tell me to approve it there. Don't print or store any token yourself.
3. Run \`ephemera doctor\` and fix anything it reports.
4. Add ephemera as an MCP server. For Claude Code: \`claude mcp add ephemera -- ephemera mcp\`. Other agents take { "command": "ephemera", "args": ["mcp"] } in their MCP settings.
5. Name each sandbox for what it's doing, like \`--name api-tests\`, so I can tell them apart in the dashboard.

Docs: ${MCP}`
