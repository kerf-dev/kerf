package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kerf-dev/kerf/internal/recommender"
)

// Output formats.
const (
	FormatTable    = "table"
	FormatCSV      = "csv"
	FormatJSON     = "json"
	FormatMarkdown = "markdown"
)

// Formats lists the output formats.
var Formats = []string{FormatTable, FormatCSV, FormatJSON, FormatMarkdown}

// InstallURL is where the full install is explained.
const InstallURL = "https://getkerf.dev/docs/install/eks"

// NextStep is the line that ends the table and Markdown reports.
const NextStep = "To keep recommendations current and apply them automatically, install Kerf: " + InstallURL

// RenderOptions tune the output.
type RenderOptions struct {
	// Limit shows the first N workloads (0 = all); totals still cover every workload.
	Limit int
	// MinSavingUSD hides workloads that save less per month.
	MinSavingUSD float64
}

// Render writes the report in the given format. The table and Markdown formats end with a line
// pointing to the full install; JSON carries it in "next"; CSV has no room for it and stays pure.
func Render(w io.Writer, r *Report, format string, ro RenderOptions) error {
	rows := r.Rows
	if ro.MinSavingUSD > 0 {
		rows = nil
		for _, x := range r.Rows {
			if x.MonthlySavingsUSD.Total >= ro.MinSavingUSD {
				rows = append(rows, x)
			}
		}
	}
	hidden := len(rows)
	if ro.Limit > 0 && len(rows) > ro.Limit {
		rows = rows[:ro.Limit]
	}
	hidden -= len(rows)
	switch format {
	case FormatTable, "":
		return renderTable(w, r, rows, hidden)
	case FormatCSV:
		return renderCSV(w, rows)
	case FormatJSON:
		return renderJSON(w, r, rows)
	case FormatMarkdown, "md":
		return renderMarkdown(w, r, rows, hidden)
	}
	return fmt.Errorf("unknown format %q (want table, csv, json or markdown)", format)
}

func cores(v float64) string  { return recommender.FormatCPU(v) }
func memory(v float64) string { return recommender.FormatMemory(v) }
func usd(v float64) string    { return fmt.Sprintf("$%.2f", v) }

func flags(f []string) string {
	if len(f) == 0 {
		return "-"
	}
	return strings.Join(f, ",")
}

func workloadName(x Row) string { return x.Kind + "/" + x.Workload }

// windowText renders the profile window ("336h0m0s") as days.
func windowText(w string) string {
	if d, err := time.ParseDuration(w); err == nil && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return w
}

func summary(r *Report) string {
	return fmt.Sprintf("Cluster %s, profile %s (%s of history), as of %s. Rates: $%.4f per vCPU-hour, $%.4f per GiB-hour (%s).",
		r.Cluster, r.Profile, windowText(r.Window), r.GeneratedAt.Format("2006-01-02 15:04 UTC"),
		r.Pricing.CPUCoreHourUSD, r.Pricing.MemoryGiBHourUSD, r.Pricing.Source)
}

func totalLine(r *Report) string {
	s := fmt.Sprintf("%d workloads, estimated saving %s per month.", r.Totals.Workloads, usd(r.Totals.MonthlySavingsUSD))
	if n := r.Totals.WithoutRecommendation; n > 0 {
		s += fmt.Sprintf(" %d more have no usage data in the window.", n)
	}
	return s
}

func renderTable(w io.Writer, r *Report, rows []Row, hidden int) error {
	if _, err := fmt.Fprintln(w, summary(r)); err != nil {
		return err
	}
	for _, m := range r.Warnings {
		fmt.Fprintln(w, "Note:", m)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tWORKLOAD\tREPLICAS\tCPU NOW\tCPU REC\tMEM NOW\tMEM REC\tSAVING/MO\tCONF\tFLAGS")
	for _, x := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%.2f\t%s\n", x.Namespace, workloadName(x), x.Replicas,
			cores(x.CPURequestCores), cores(x.CPURecommendedCores), memory(x.MemoryRequestBytes), memory(x.MemoryRecommendedBytes),
			usd(x.MonthlySavingsUSD.Total), x.Confidence, flags(x.RiskFlags))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w)
	if hidden > 0 {
		fmt.Fprintf(w, "(%d more workloads not shown.)\n", hidden)
	}
	fmt.Fprintln(w, totalLine(r))
	_, err := fmt.Fprintln(w, NextStep)
	return err
}

func renderMarkdown(w io.Writer, r *Report, rows []Row, hidden int) error {
	fmt.Fprintf(w, "# Kerf report: %s\n\n%s\n\n", r.Cluster, summary(r))
	for _, m := range r.Warnings {
		fmt.Fprintf(w, "> Note: %s\n", m)
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "| Namespace | Workload | Replicas | CPU now | CPU rec | Memory now | Memory rec | Saving/month | Confidence | Flags |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|---:|---:|---|")
	esc := func(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
	for _, x := range rows {
		fmt.Fprintf(w, "| %s | %s | %.1f | %s | %s | %s | %s | %s | %.2f | %s |\n", esc(x.Namespace), esc(workloadName(x)), x.Replicas,
			cores(x.CPURequestCores), cores(x.CPURecommendedCores), memory(x.MemoryRequestBytes), memory(x.MemoryRecommendedBytes),
			usd(x.MonthlySavingsUSD.Total), x.Confidence, flags(x.RiskFlags))
	}
	fmt.Fprintln(w)
	if hidden > 0 {
		fmt.Fprintf(w, "_%d more workloads not shown._\n\n", hidden)
	}
	fmt.Fprintf(w, "**%s**\n\n", totalLine(r))
	_, err := fmt.Fprintf(w, "%s\n", NextStep)
	return err
}

func renderCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"namespace", "kind", "workload", "average_replicas", "cpu_request_cores", "cpu_recommended_cores",
		"memory_request_bytes", "memory_recommended_bytes", "monthly_saving_usd", "cpu_saving_usd", "memory_saving_usd", "confidence", "risk_flags"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, x := range rows {
		_ = cw.Write([]string{x.Namespace, x.Kind, x.Workload, f(x.Replicas), f(x.CPURequestCores), f(x.CPURecommendedCores),
			f(x.MemoryRequestBytes), f(x.MemoryRecommendedBytes), f(x.MonthlySavingsUSD.Total), f(x.MonthlySavingsUSD.CPU),
			f(x.MonthlySavingsUSD.Memory), f(x.Confidence), strings.Join(x.RiskFlags, ";")})
	}
	cw.Flush()
	return cw.Error()
}

func renderJSON(w io.Writer, r *Report, rows []Row) error {
	out := struct {
		*Report
		Workloads []Row  `json:"workloads"`
		Next      string `json:"next"`
	}{Report: r, Workloads: rows, Next: NextStep}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
