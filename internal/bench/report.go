package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// labels are the human names of the measurements.
var labels = map[string]string{
	ColdBoot:        "Cold boot",
	TemplateBuild:   "Template snapshot",
	Restore:         "Restore from snapshot",
	RunEndToEnd:     "ephemera run, end to end",
	FirstByte:       "First output byte",
	LineDelay:       "Line delay, p99",
	SetupFirstRun:   "Setup run (+ save)",
	SetupSnapshot:   "Run from setup snapshot",
	DensityStartAll: "Density: start all",
	DensityMemory:   "Density: host memory",
	DensityPSS:      "Density: PSS",
}

// Load reads a report written by Save.
func Load(path string) (*Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// Save writes the report as indented JSON.
func Save(r *Report, path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Change is a result's difference from an earlier run, in percent;
// positive is slower or bigger. ok is false when the earlier run lacks it.
func Change(r Result, prev *Report) (float64, bool) {
	if prev == nil {
		return 0, false
	}
	for _, p := range prev.Results {
		if p.Name == r.Name && p.Unit == r.Unit && p.Value > 0 {
			return (r.Value - p.Value) / p.Value * 100, true
		}
	}
	return 0, false
}

// Print writes the report as a table, with each result's change from
// prev when given.
func Print(w io.Writer, r *Report, prev *Report) {
	m := r.Machine
	virt := "bare metal"
	if m.Virtualized {
		virt = "virtualized (nested)"
	}
	fmt.Fprintf(w, "ephemera benchmark  commit %s  %s\n", short(r.Commit), r.Date.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(w, "%s: %s, %d CPUs, %d MB, %s, kernel %s\n", m.Hostname, m.CPU, m.CPUs, m.MemoryMB, virt, m.Kernel)
	fmt.Fprintf(w, "sandboxes: %d vCPUs, %d MB, jailed %v\n", r.Config.CPUs, r.Config.MemoryMB, r.Config.Jailed)
	if prev != nil {
		fmt.Fprintf(w, "compared with commit %s from %s\n", short(prev.Commit), prev.Date.Format("2006-01-02"))
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := "MEASUREMENT\tMEDIAN\tP90\tMAX\tN"
	if prev != nil {
		head += "\tCHANGE"
	}
	fmt.Fprintln(tw, head)
	for _, res := range r.Results {
		name := labels[res.Name]
		if name == "" {
			name = res.Name
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", name, num(res.Value, res.Unit), num(res.P90, res.Unit), num(res.Max, res.Unit), count(res.N))
		if prev != nil {
			if c, ok := Change(res, prev); ok {
				line += fmt.Sprintf("\t%+.0f%%", c)
			} else {
				line += "\tnew"
			}
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
}

func num(v float64, unit string) string {
	if v == 0 {
		return "-"
	}
	if unit == "ms" && v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".") + " " + unit
}

func count(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	if commit == "" {
		return "unknown"
	}
	return commit
}
