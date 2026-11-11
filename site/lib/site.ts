export const GITHUB = "https://github.com/pyjeebz/ephemera"
export const INSTALL = "curl -fsSL https://ephemera.run/install | sh"

// The docs live in the repo for now, so every docs link points at GitHub.
const BLOB = `${GITHUB}/blob/main`
export const DOCS = `${GITHUB}#readme`
export const INSTALL_DOCS = `${GITHUB}#install-the-cli`
export const HOW_IT_WORKS = `${GITHUB}#how-it-works`
export const SNAPSHOTS = `${GITHUB}#setup-snapshots`
export const MCP = `${GITHUB}#coding-agents-mcp`
export const BENCHMARKS = `${GITHUB}#benchmarks`
export const SELF_HOST = `${BLOB}/docs/self-hosting.md`
export const SECURITY = `${BLOB}/docs/self-hosting.md#security-notes`
export const CHANGELOG = `${BLOB}/CHANGELOG.md`
export const LICENSE = `${BLOB}/LICENSE`

// What "copy the prompt" puts on the clipboard: enough for a coding agent to
// install the CLI and add ephemera as an MCP server, without seeing the token.
export const AGENT_PROMPT = `Set up ephemera for this project, so your shell commands run in throwaway sandboxes instead of on my machine.

1. Install the CLI: ${INSTALL}
2. Ask me for my ephemera server URL. Then tell me to run \`ephemera login --server <URL>\` myself and paste my API token there. Don't ask for the token or print it.
3. Run \`ephemera doctor\` and fix anything it reports.
4. Add ephemera as an MCP server. For Claude Code: \`claude mcp add ephemera -- ephemera mcp\`. Other agents take { "command": "ephemera", "args": ["mcp"] } in their MCP settings.

Docs: ${MCP}`
