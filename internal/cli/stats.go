package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"ephemera.run/ephemera/internal/playback"
)

// runStats shows whether links feel instant: what receiver pages
// reported about playback, next to what the relay saw of the senders
// and how much it served from its own copy.
func runStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	client := adminFlags(fs)
	n := fs.Int("n", 20, "list at most this many recent playbacks (0 = none)")
	asJSON := fs.Bool("json", false, "print the raw JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if _, err := c.do("GET", fmt.Sprintf("/admin/playback?limit=%d", max(*n, 0)), nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		_, err := os.Stdout.Write(append(raw, '\n'))
		return err
	}
	var pb struct {
		Summary playback.Summary
		Views   []playback.View
	}
	if err := json.Unmarshal(raw, &pb); err != nil {
		return err
	}
	// the metrics snapshot mixes counters with expvar's own cmdline
	// and memstats; keep the numbers
	var snapshot map[string]any
	if _, err := c.do("GET", "/admin/metrics", nil, &snapshot); err != nil {
		return err
	}
	counters := map[string]float64{}
	for k, v := range snapshot {
		if f, ok := v.(float64); ok {
			counters[k] = f
		}
	}
	printStats(os.Stdout, pb.Summary, pb.Views, counters)
	return nil
}

func printStats(w io.Writer, s playback.Summary, views []playback.View, counters map[string]float64) {
	fmt.Fprintf(w, "uploads: %.0f finished, %s on the relay's disk, %.0f turned away for lack of room\n",
		counters["uploads_completed"], sizeMB(int64(counters["upload_bytes_held"])), counters["uploads_refused"])
	if s.Views == 0 {
		fmt.Fprintln(w, "playbacks: none reported yet (receiver pages report when they're closed or hidden)")
		return
	}
	var states []string
	for _, st := range []string{"live", "available", "offline"} {
		if k := s.ByState[st]; k > 0 {
			states = append(states, fmt.Sprintf("%s %d", st, k))
		}
	}
	fmt.Fprintf(w, "playbacks: %d (%s)\n", s.Views, strings.Join(states, ", "))
	fmt.Fprintf(w, "first frame: %s median, %s p90\n", ms(s.TTFFMsP50), ms(s.TTFFMsP90))
	if s.StartMsP50 > 0 {
		fmt.Fprintf(w, "play to playing: %s median\n", ms(s.StartMsP50))
	}
	fmt.Fprintf(w, "stalls: %d%% of playbacks never stalled; %.1f%% of watch time spent stalled\n", s.StallFreePct, s.RebufferPct)
	if s.SeekWaitMsP50 > 0 {
		fmt.Fprintf(w, "seeks: %s median wait\n", ms(s.SeekWaitMsP50))
	}
	if s.BitrateBpsP50 > 0 {
		fmt.Fprintf(w, "speed: files play at %s median", mbps(s.BitrateBpsP50))
		if s.SenderBpsP50 > 0 {
			fmt.Fprintf(w, ", senders deliver live at %s median; sender slower than the file in %d%% of playbacks",
				mbps(s.SenderBpsP50), s.SlowSenderPct)
		}
		fmt.Fprintln(w)
	}
	if len(views) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tLINK\tSTATE\tFIRST FRAME\tSTALLS\tSEEK WAIT\tFILE\tSENDER\tFROM RELAY")
	for _, v := range views {
		seek := "-"
		if v.Seeks > 0 {
			seek = ms(v.SeekWaitMs / v.Seeks)
		}
		stalls := "0"
		if v.Stalls > 0 {
			stalls = fmt.Sprintf("%d (%s)", v.Stalls, ms(v.StallMs))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d%%\n",
			v.At.Local().Format("01-02 15:04"), v.Share, v.State, ms(v.TTFFMs), stalls, seek,
			mbps(v.BitrateBps), mbps(v.SenderBps), v.FromRelayPct)
	}
	tw.Flush()
}

// ms renders milliseconds for people.
func ms(v int64) string {
	if v < 1000 {
		return fmt.Sprintf("%dms", v)
	}
	return fmt.Sprintf("%.1fs", float64(v)/1000)
}

// mbps renders bits per second as megabits, "-" for unknown.
func mbps(bps int64) string {
	if bps <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f Mbps", float64(bps)/1e6)
}

func init() {
	register("stats", runStats)
}
