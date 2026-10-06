// Command trail inspects reconstructed Trail capture journals.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/internal/query"
	"go.lostcrafters.com/trail/internal/render"
	"go.lostcrafters.com/trail/internal/replay"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, diagnostics io.Writer) int {
	return runWithOpen(args, out, diagnostics, func(path string) (io.ReadCloser, error) { return os.Open(path) })
}

func trailCommand(out, diagnostics io.Writer, open func(string) (io.ReadCloser, error), observe func(replay.RetentionStats)) *cli.Command {
	app := &cli.Command{
		Name: "trail", Usage: "Inspect and query explicit Trail capture journals",
		Description: "Read native Trail journal files. No stdin, globbing or capture discovery.\nUse 'trail help <command>' for command-specific options and examples.",
		Writer:      out, ErrWriter: diagnostics, Suggest: true, DisableSliceFlagSeparator: true,
		// Return exit errors to runWithOpen; only main owns process termination.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		OnUsageError:   func(_ context.Context, _ *cli.Command, err error, _ bool) error { return cli.Exit(err, 1) },
		Action: func(_ context.Context, c *cli.Command) error {
			if c.Args().Len() > 0 {
				return cli.Exit(fmt.Sprintf("unknown command %q; use trail --help", c.Args().First()), 1)
			}
			if err := cli.ShowRootCommandHelp(c); err != nil {
				return err
			}
			return cli.Exit("a command is required", 1)
		},
	}
	for _, spec := range []struct{ name, usage, description, args string }{
		{"inspect", "Summarize a capture and highlight problems", "Whole-capture totals with bounded slowest, error and incomplete summaries.\nExample: trail inspect capture.trail.jsonl --top 5", "<file>"},
		{"trace", "Show a trace by full trace ID", "Reconstruct one trace. Text output includes its span tree and events.\nExample: trail trace capture.trail.jsonl <32-character-trace-id> --max-depth 4", "<file> <trace-id>"},
		{"span", "Show a span by full span ID", "Show the span's timing, status, typed attributes, events and ancestry.", "<file> <span-id>"},
		{"query", "Select spans or traces using AND predicates", "All filters use AND; attribute equality requires an explicit type.\nExample: trail query capture.trail.jsonl --status error --attribute retries=int:2 --format ndjson", "<file>"},
		{"stats", "Show aggregate counts, durations and loss accounting", "Aggregate the whole capture without retaining completed trace payloads.", "<file>"},
		{"export", "Export reconstructed capture data", "JSON/TOON retain the complete document; NDJSON/compact emit trace-finalized items incrementally.\nExample: trail export capture.trail.jsonl --format ndjson", "<file>"},
	} {
		app.Commands = append(app.Commands, &cli.Command{Name: spec.name, Usage: spec.usage, Description: spec.description, ArgsUsage: spec.args, Flags: commandFlags(spec.name), DisableSliceFlagSeparator: true,
			// Leaf commands have file/ID arguments, not nested help topics.
			// Also show their help when --help follows those arguments.
			CommandNotFound: func(ctx context.Context, c *cli.Command, _ string) { _ = cli.ShowCommandHelp(ctx, c.Root(), c.Name) },
			Action: func(_ context.Context, c *cli.Command) error {
				code := executeCommand(c, out, diagnostics, open, observe)
				if code == 0 {
					return nil
				}
				if code == 1 {
					return cli.Exit("invalid arguments; use trail "+c.Name+" --help", 1)
				}
				return cli.Exit("", code)
			}})
	}
	return app
}

func commandFlags(command string) []cli.Flag {
	all := []cli.Flag{
		&cli.StringFlag{Name: "format", Value: "text", Usage: "Output `FORMAT`: text, compact, json, ndjson, toon", Category: "Output"},
		&cli.StringFlag{Name: "color", Value: "never", Usage: "Text color `MODE`: auto, never, always (text only)", Category: "Output"},
		&cli.BoolFlag{Name: "local-time", Usage: "Display human wall timestamps in local time (text only)", Category: "Output"},
		&cli.BoolFlag{Name: "best-effort", Usage: "Recover eligible bad records with explicit diagnostics", Category: "Replay"},
		&cli.BoolFlag{Name: "strict", Usage: "Reject replay diagnostics", Category: "Replay"},
		&cli.BoolFlag{Name: "fail-on-error", Usage: "Exit 5 for recorded Trail Error status", Category: "Replay"},
		&cli.StringFlag{Name: "status", Usage: "Final Trail `STATUS`: unset, ok, error", Category: "Filters"},
		&cli.StringFlag{Name: "scope", Usage: "Match scope `PREFIX` (case-sensitive)", Category: "Filters"},
		&cli.StringFlag{Name: "name", Usage: "Match name `TEXT` (case-insensitive substring)", Category: "Filters"},
		&cli.BoolFlag{Name: "incomplete", Usage: "Select spans with no end (decided at EOF)", Category: "Filters"},
		&cli.StringFlag{Name: "trace-id", Usage: "Match full 32-character trace `ID`", Category: "Filters"},
		&cli.StringFlag{Name: "span-id", Usage: "Match full 16-character span `ID`", Category: "Filters"},
		&cli.StringFlag{Name: "mode", Value: "spans", Usage: "Result `MODE`: spans or traces", Category: "Results"},
		&cli.IntFlag{Name: "top", Usage: "Keep longest `N` results; inspect 0 shows 10 summaries", Category: "Results"},
		&cli.StringSliceFlag{Name: "attribute", Usage: "Typed equality `KEY=TYPE:VALUE` (repeatable; int, uint, duration, float, bool, str)", Category: "Filters"},
		&cli.StringFlag{Name: "min-duration", Usage: "Minimum authoritative `DURATION`, e.g. 25ms", Category: "Filters"},
		&cli.StringFlag{Name: "since", Usage: "Inclusive elapsed start `DURATION`; inspect restricts summaries only", Category: "Filters"},
		&cli.StringFlag{Name: "until", Usage: "Inclusive elapsed start `DURATION`; inspect restricts summaries only", Category: "Filters"},
		&cli.IntFlag{Name: "max-depth", Value: 10, Usage: "Maximum text tree `DEPTH` (root is 0)", Category: "Tree"},
		&cli.IntFlag{Name: "max-spans", Value: 500, Usage: "Maximum text tree span `COUNT`", Category: "Tree"},
		&cli.BoolFlag{Name: "no-events", Usage: "Hide events in text trace output", Category: "Tree"},
		&cli.StringFlag{Name: "sort", Value: "start", Usage: "Sort by `ORDER`: start or duration; explicit query sorting waits for EOF", Category: "Results"},
	}
	flags := make([]cli.Flag, 0, len(all))
	for _, f := range all {
		if supportsFlag(command, "text", f.Names()[0]) {
			flags = append(flags, f)
		}
	}
	return flags
}

func runWithOpen(args []string, out, diagnostics io.Writer, open func(string) (io.ReadCloser, error)) int {
	return runObserved(args, out, diagnostics, open, nil)
}

func runObserved(args []string, out, diagnostics io.Writer, open func(string) (io.ReadCloser, error), observe func(replay.RetentionStats)) int {
	app := trailCommand(out, diagnostics, open, observe)
	err := app.Run(context.Background(), append([]string{"trail"}, args...))
	if err == nil {
		return 0
	}
	if err.Error() != "" {
		_, _ = fmt.Fprintln(diagnostics, err)
	}
	var exit cli.ExitCoder
	if errors.As(err, &exit) {
		if exit.ExitCode() == 3 && err.Error() != "" {
			return 1
		}
		return exit.ExitCode()
	}
	return 1
}

func executeCommand(cmd *cli.Command, out, diagnostics io.Writer, open func(string) (io.ReadCloser, error), observe func(replay.RetentionStats)) int {
	command := cmd.Name
	want := 1
	if command == "trace" || command == "span" {
		want = 2
	}
	if cmd.Args().Len() != want {
		_, _ = fmt.Fprintf(diagnostics, "trail %s requires %s; use trail %s --help\n", command, cmd.ArgsUsage, command)
		return 1
	}
	path, id := cmd.Args().Get(0), cmd.Args().Get(1)
	if command == "trace" {
		if _, err := trail.ParseTraceID(id); err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 1
		}
	}
	if command == "span" {
		if _, err := trail.ParseSpanID(id); err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 1
		}
	}
	format, best, strict, fail := new(cmd.String("format")), new(cmd.Bool("best-effort")), new(cmd.Bool("strict")), new(cmd.Bool("fail-on-error"))
	color, localTime := new(cmd.String("color")), new(cmd.Bool("local-time"))
	status, scope, name, traceID, spanID, minDuration := new(""), new(""), new(""), new(""), new(""), new("")
	incomplete, mode, top := new(false), new("spans"), new(0)
	since, until := new(""), new("")
	maxDepth, maxSpans, noEvents, sortOrder := new(10), new(500), new(false), new("start")
	var attributes []string
	sortExplicit := false
	if command == "inspect" || command == "query" {
		*top = cmd.Int("top")
		*since = cmd.String("since")
		*until = cmd.String("until")
	}
	if command == "trace" {
		*maxDepth = cmd.Int("max-depth")
		*maxSpans = cmd.Int("max-spans")
		*noEvents = cmd.Bool("no-events")
	}
	if command == "trace" || command == "query" {
		*sortOrder = cmd.String("sort")
		sortExplicit = cmd.IsSet("sort")
	}
	if command == "query" {
		*status = cmd.String("status")
		*scope = cmd.String("scope")
		*name = cmd.String("name")
		*traceID = cmd.String("trace-id")
		*spanID = cmd.String("span-id")
		*minDuration = cmd.String("min-duration")
		*incomplete = cmd.Bool("incomplete")
		*mode = cmd.String("mode")
		attributes = cmd.StringSlice("attribute")
	}
	for _, flag := range cmd.LocalFlagNames() {
		if !supportsFlag(command, *format, flag) {
			_, _ = fmt.Fprintf(diagnostics, "--%s is not supported by %s --format %s\n", flag, command, *format)
			return 1
		}
	}
	if !slices.Contains([]string{"text", "compact", "json", "ndjson", "toon"}, *format) || *top < 0 || (*mode != "spans" && *mode != "traces") {
		return 1
	}
	if *maxDepth < 0 || *maxSpans < 1 || !slices.Contains([]string{"start", "duration"}, *sortOrder) || !slices.Contains([]string{"never", "auto", "always"}, *color) {
		return 1
	}
	filter := query.Filter{Status: *status, Scope: *scope, Name: *name, Incomplete: *incomplete, TraceID: strings.ToLower(*traceID), SpanID: strings.ToLower(*spanID), Attributes: attributes}
	for _, pair := range []struct {
		text string
		dest **int64
	}{{*minDuration, &filter.MinDuration}, {*since, &filter.Since}, {*until, &filter.Until}} {
		if pair.text != "" {
			d, e := time.ParseDuration(pair.text)
			if e != nil || d < 0 {
				return 1
			}
			n := int64(d)
			*pair.dest = &n
		}
	}
	if filter.Since != nil && filter.Until != nil && *filter.Since > *filter.Until {
		return 1
	}
	if filter.TraceID != "" {
		if _, e := trail.ParseTraceID(filter.TraceID); e != nil {
			return 1
		}
	}
	if filter.SpanID != "" {
		if _, e := trail.ParseSpanID(filter.SpanID); e != nil {
			return 1
		}
	}
	if err := filter.Validate(); err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 1
	}
	f, err := open(path)
	if err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 2
	}
	defer func() { _ = f.Close() }()
	options := replay.Options{BestEffort: *best, Strict: *strict}
	if observe != nil {
		options.Retention = &replay.RetentionStats{}
	}
	var selectedID trail.TraceID
	if command == "trace" || command == "span" {
		options.ReleaseCompleted = true
		options.DiscardSpanPayload = true
		options.OnTrace = func(t *replay.MachineTrace) error {
			if command == "trace" && strings.EqualFold(t.TraceID.String(), id) {
				selectedID = t.TraceID
			}
			if command == "span" {
				sid, _ := trail.ParseSpanID(id)
				if t.Spans[sid] != nil {
					selectedID = t.TraceID
				}
			}
			return nil
		}
	}
	streamed := command == "export" && (*format == "ndjson" || *format == "compact")
	streamQuery := command == "query" && *mode == "spans" && *format == "ndjson" && *top == 0 && !sortExplicit
	matchedItems := 0
	queryItems := []render.Object{}
	hadError := false
	overview := command == "inspect" || command == "stats"
	slow := []render.Object{}
	errorSpans := []render.Object{}
	withinWindow := func(start int64) bool {
		return (filter.Since == nil || start >= *filter.Since) && (filter.Until == nil || start <= *filter.Until)
	}
	if overview {
		options.DiscardSpanPayload = true
		options.OnSpanEnded = func(s *replay.MachineSpan) error {
			if s.Status != nil && s.Status.Code == replay.StatusError && withinWindow(s.StartElapsedNanos) {
				hadError = true
				limit := *top
				if limit == 0 {
					limit = 10
				}
				if len(errorSpans) < limit {
					errorSpans = append(errorSpans, render.Object{"span_id": s.SpanID.String(), "trace_id": s.TraceID.String(), "name": s.Name, "description": s.Status.Description})
				}
			}
			return nil
		}
		options.ReleaseCompleted = true
		options.OnTrace = func(t *replay.MachineTrace) error {
			for _, s := range render.OrderedSpans(t) {
				if s.Status != nil && s.Status.Code == replay.StatusError {
					hadError = true
					if !s.Ended && withinWindow(s.StartElapsedNanos) {
						limit := *top
						if limit == 0 {
							limit = 10
						}
						if len(errorSpans) < limit {
							errorSpans = append(errorSpans, render.Object{"span_id": s.SpanID.String(), "trace_id": s.TraceID.String(), "name": s.Name, "description": s.Status.Description})
						}
					}
				}
			}
			if command == "inspect" && t.TraceLifetimeNanos != nil && t.RootSpan != nil {
				start := t.RootSpan.StartElapsedNanos
				if filter.Since != nil && start < *filter.Since || filter.Until != nil && start > *filter.Until {
					return nil
				}
				slow = append(slow, render.Object{"trace_id": t.TraceID.String(), "root_name": t.RootSpan.Name, "trace_lifetime_nanos": *t.TraceLifetimeNanos})
				slices.SortFunc(slow, func(a, b render.Object) int {
					an, _ := strconv.ParseInt(a["trace_lifetime_nanos"].(string), 10, 64)
					bn, _ := strconv.ParseInt(b["trace_lifetime_nanos"].(string), 10, 64)
					if an > bn {
						return -1
					}
					if an < bn {
						return 1
					}
					return strings.Compare(a["trace_id"].(string), b["trace_id"].(string))
				})
				limit := *top
				if limit == 0 {
					limit = 10
				}
				if len(slow) > limit {
					slow = slow[:limit]
				}
			}
			return nil
		}
	}
	if streamed {
		options.ReleaseCompleted = true
		options.OnTrace = func(t *replay.MachineTrace) error {
			for _, s := range t.Spans {
				if s.Status != nil && s.Status.Code == replay.StatusError {
					hadError = true
				}
			}
			if *format == "compact" {
				return render.Write(out, "compact", render.Object{"trace": render.Trace(t)})
			}
			return render.WriteTraceItems(out, t)
		}
	}
	if streamQuery {
		options.ReleaseCompleted = true
		options.OnTrace = func(t *replay.MachineTrace) error {
			for _, s := range render.OrderedSpans(t) {
				if s.Status != nil && s.Status.Code == replay.StatusError {
					hadError = true
				}
				if filter.Match(s) {
					matchedItems++
					o := render.Span(s)
					o["type"] = "span"
					if err := render.Write(out, "ndjson", o); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	if command == "query" && !streamQuery {
		options.ReleaseCompleted = true
		options.OnTrace = func(t *replay.MachineTrace) error {
			matched := false
			for _, s := range render.OrderedSpans(t) {
				if s.Status != nil && s.Status.Code == replay.StatusError {
					hadError = true
				}
				if filter.Match(s) {
					matched = true
					if *mode == "spans" {
						queryItems = retainResult(queryItems, render.Span(s), *top, *top > 0 || *sortOrder == "duration", false)
					}
				}
			}
			if matched && *mode == "traces" {
				queryItems = retainResult(queryItems, render.Trace(t), *top, *top > 0 || *sortOrder == "duration", true)
			}
			return nil
		}
	}
	firstHash := sha256.New()
	source := io.Reader(f)
	if command == "trace" || command == "span" {
		source = io.TeeReader(f, firstHash)
	}
	c, err := replay.Read(source, path, options)
	if observe != nil {
		observe(*options.Retention)
	}
	if err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 2
	}
	if command == "trace" || command == "span" || *format == "compact" || command == "query" {
		for _, d := range c.Diagnostics {
			_, _ = fmt.Fprintf(diagnostics, "replay %s line %d: %s\n", d.Type, d.Line, d.Message)
		}
		if tail := c.TailCondition; tail != nil {
			_, _ = fmt.Fprintf(diagnostics, "replay %s line %d: %s\n", tail.Type, tail.Line, tail.Message)
		}
	}
	if command == "trace" || command == "span" {
		if !selectedID.IsValid() {
			return 3
		}
		second, err := open(path)
		if err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 2
		}
		secondOptions := replay.Options{BestEffort: *best, Strict: *strict, TargetTrace: selectedID}
		if observe != nil {
			secondOptions.Retention = &replay.RetentionStats{}
		}
		if command == "span" {
			secondOptions.PayloadSpan, _ = trail.ParseSpanID(id)
		}
		secondHash := sha256.New()
		target, readErr := replay.Read(io.TeeReader(second, secondHash), path, secondOptions)
		if observe != nil {
			observe(*secondOptions.Retention)
		}
		closeErr := second.Close()
		if readErr != nil {
			_, _ = fmt.Fprintln(diagnostics, readErr)
			return 2
		}
		if closeErr != nil {
			_, _ = fmt.Fprintln(diagnostics, closeErr)
			return 2
		}
		if !bytes.Equal(firstHash.Sum(nil), secondHash.Sum(nil)) {
			_, _ = fmt.Fprintln(diagnostics, "capture changed between replay passes")
			return 2
		}
		c.Traces = target.Traces
	}
	if streamed {
		summary := render.Capture(c, false)
		delete(summary, "incomplete_traces")
		if err := render.Write(out, *format, render.Object{"capture": summary}); err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 2
		}
		if *fail && hadError {
			return 5
		}
		return 0
	}
	if streamQuery {
		if err := render.Write(out, "ndjson", render.Object{"type": "query_summary", "matched": strconv.Itoa(matchedItems), "diagnostics": render.Capture(c, false)["diagnostics"]}); err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 2
		}
		if matchedItems == 0 {
			return 4
		}
		if *fail && hadError {
			return 5
		}
		return 0
	}
	document := render.Object{}
	switch command {
	case "inspect", "stats":
		document["capture"] = render.Capture(c, false)
		if command == "stats" {
			delete(document["capture"].(render.Object), "incomplete_traces")
		}
		if command == "inspect" {
			capture := document["capture"].(render.Object)
			if filter.Since != nil || filter.Until != nil {
				window := render.Object{"applies_to": "detail_summaries", "totals": "whole_capture"}
				if filter.Since != nil {
					window["since_nanos"] = strconv.FormatInt(*filter.Since, 10)
				}
				if filter.Until != nil {
					window["until_nanos"] = strconv.FormatInt(*filter.Until, 10)
				}
				capture["detail_window"] = window
			}
			capture["slowest_traces"] = slow
			capture["error_spans"] = errorSpans
			unfinished := []render.Object{}
			limit := *top
			if limit == 0 {
				limit = 10
			}
			for _, item := range capture["incomplete_traces"].([]render.Object) {
				tid, _ := trail.ParseTraceID(item["trace_id"].(string))
				t := c.Traces[tid]
				if (filter.Since != nil || filter.Until != nil) && (t.RootSpan == nil || !withinWindow(t.RootSpan.StartElapsedNanos)) {
					continue
				}
				if len(unfinished) < limit {
					unfinished = append(unfinished, item)
				}
			}
			capture["incomplete_traces"] = unfinished
		}
	case "export":
		document["capture"] = render.Capture(c, true)
	case "trace":
		tid, _ := trail.ParseTraceID(id)
		t := c.Traces[tid]
		if t == nil {
			return 3
		}
		document["trace"] = render.Trace(t)
	case "span":
		found := false
		for _, t := range c.Traces {
			for _, s := range t.Spans {
				if s.SpanID.String() == strings.ToLower(id) {
					document["span"] = render.Span(s)
					found = true
				}
			}
		}
		if !found {
			return 3
		}
	case "query":
		document["diagnostics"] = render.Capture(c, false)["diagnostics"]
		slices.SortFunc(queryItems, func(a, b render.Object) int {
			return compareResults(a, b, *top > 0 || *sortOrder == "duration", *mode == "traces")
		})
		if len(queryItems) == 0 {
			return 4
		}
		if *mode == "traces" {
			document["traces"] = queryItems
		} else {
			document["spans"] = queryItems
		}
	}
	if *color == "auto" {
		*color = "never"
		if file, ok := out.(*os.File); ok {
			if info, e := file.Stat(); e == nil && info.Mode()&os.ModeCharDevice != 0 {
				*color = "always"
			}
		}
	}
	if err := render.WriteWithOptions(out, *format, document, render.TextOptions{MaxDepth: *maxDepth, MaxSpans: *maxSpans, NoEvents: *noEvents, Sort: *sortOrder, LocalTime: *localTime, Color: *color}); err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 2
	}
	if *fail {
		if hadError || c.EndedError > 0 {
			return 5
		}
		for _, t := range c.Traces {
			for _, s := range t.Spans {
				if s.Status != nil && s.Status.Code == replay.StatusError {
					return 5
				}
			}
		}
	}
	return 0
}

func supportsFlag(command, format, name string) bool {
	switch name {
	case "status", "scope", "name", "incomplete", "trace-id", "span-id", "mode", "attribute", "min-duration":
		return command == "query"
	case "since", "until", "top":
		return command == "query" || command == "inspect"
	case "max-depth", "max-spans", "no-events":
		return command == "trace" && format == "text"
	case "sort":
		return command == "trace" && format == "text" || command == "query"
	case "color", "local-time":
		return format == "text"
	default:
		return true
	}
}

func compareResults(a, b render.Object, duration bool, trace bool) int {
	key, id := "start_elapsed_nanos", "span_id"
	if trace {
		key, id = "trace_lifetime_nanos", "trace_id"
	}
	if duration {
		if !trace {
			key = "duration_nanos"
		}
		av, aok := a[key].(string)
		bv, bok := b[key].(string)
		if aok != bok {
			if aok {
				return -1
			}
			return 1
		}
		an, _ := strconv.ParseInt(av, 10, 64)
		bn, _ := strconv.ParseInt(bv, 10, 64)
		if an > bn {
			return -1
		}
		if an < bn {
			return 1
		}
	} else if !trace {
		an, _ := strconv.ParseInt(a[key].(string), 10, 64)
		bn, _ := strconv.ParseInt(b[key].(string), 10, 64)
		if an < bn {
			return -1
		}
		if an > bn {
			return 1
		}
	}
	return strings.Compare(a[id].(string), b[id].(string))
}

func retainResult(items []render.Object, item render.Object, top int, duration, trace bool) []render.Object {
	items = append(items, item)
	if top > 0 {
		slices.SortFunc(items, func(a, b render.Object) int { return compareResults(a, b, duration, trace) })
		if len(items) > top {
			items[len(items)-1] = nil
			items = items[:top]
		}
	}
	return items
}
