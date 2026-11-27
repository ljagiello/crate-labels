package ipp

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ljagiello/crate-labels/internal/driver/internal/page"
	proto "github.com/ljagiello/crate-labels/internal/ipp"
	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/sheets"
)

const (
	// defaultPoll is how often a job is polled unless ipp_poll_s says otherwise.
	defaultPoll = 2 * time.Second
	// minPoll is the shortest ipp_poll_s honoured, so a bad setting cannot flood the
	// printer with requests.
	minPoll = 250 * time.Millisecond
	// jobPatience is how long a job may sit without visible progress before the run
	// moves on and reports it as sent but unconfirmed (see proto.Watch). A page printer
	// prints a label page in seconds, so this is ample.
	jobPatience = 90 * time.Second
	// cancelTimeout bounds the Cancel-Job sent after the caller gives up.
	cancelTimeout = 5 * time.Second
)

func init() { printer.Register(&Driver{}) }

// Driver prints to IPP printers. The zero value is ready to use.
type Driver struct {
	// HTTP replaces the IPP HTTP client (tests); nil uses proto.NewHTTPClient.
	HTTP *http.Client
	// pollFloor replaces minPoll in tests.
	pollFloor time.Duration
	// patience replaces jobPatience in tests.
	patience time.Duration
}

// Family implements printer.Driver.
func (*Driver) Family() string { return "ipp" }

// Links implements printer.Driver: the driver talks to Target.Address itself.
func (*Driver) Links() []printer.LinkKind { return []printer.LinkKind{printer.LinkIPP} }

// Canvas implements printer.Driver: the design size at the model's dpi (300 by default;
// set the model's dpi to the printer's resolution to render at it).
func (*Driver) Canvas(m *printer.Model, s *printer.Stock) printer.Canvas {
	return printer.DefaultCanvas(m, s)
}

// Preview implements printer.Driver. Sheet stocks show the imposed first page (with
// trim marks where the sheet is cut by hand); other stocks print one grey label per
// page at actual size, so the preview is the label itself.
func (*Driver) Preview(m *printer.Model, s *printer.Stock, label *image.Gray, settings printer.Settings) (*image.Gray, error) {
	if s.Sheet == nil {
		return sheets.Normalize(label), nil
	}
	return sheets.Impose([]*image.Gray{label}, s, m.DPI, page.CropMarks(s, settings))[0], nil
}

// pollInterval is ipp_poll_s, never below minPoll.
func (d *Driver) pollInterval(s printer.Settings) time.Duration {
	floor := minPoll
	if d.pollFloor > 0 {
		floor = d.pollFloor
	}
	return max(floor, time.Duration(s.Float("ipp_poll_s", defaultPoll.Seconds())*float64(time.Second)))
}

// Send implements printer.Driver. It asks the printer what it takes, plans the jobs,
// checks them, then prints and watches each job.
//
// While a job waits on a stopped printer (out of paper, a jam, an open cover) Send keeps
// watching and reports the printer's condition as progress, so the job finishes once
// someone fixes it. When ctx ends while a job is printing, Send cancels that job on the
// printer before returning.
//
// Failures before anything is printed are returned as errors. When a job fails after
// others printed, Send returns the partial Result (OK false, Printed = labels believed
// printed) together with a *printer.Error carrying the same message.
func (d *Driver) Send(ctx context.Context, pc *printer.PrintContext, labels []*image.Gray) (printer.Result, error) {
	if len(labels) == 0 {
		return printer.Result{OK: true, Message: "nothing to print"}, nil
	}
	ep, err := proto.ParseEndpoint(pc.Target.Address)
	if err != nil {
		return printer.Result{}, printer.Errorf("The address saved for %q cannot be used (%v). Pick the printer from the network scan, or enter one such as ipp://198.51.100.9:631/ipp/print.", pc.Target.Name, err)
	}
	dryRun := page.Flag(pc.Settings, "dry_run", false)
	client := proto.NewClient(ep, !dryRun)
	client.User = pc.Settings.String("ipp_user", proto.DefaultUser)
	client.HTTP = d.HTTP
	defer client.CloseIdle()
	who := pc.Target.Name
	if who == "" {
		who = ep.Authority()
	}

	pc.Report(0, "Reading what "+who+" supports")
	p, err := client.GetPrinterAttributes(ctx)
	if err != nil {
		var se *proto.StatusError
		switch {
		case ctx.Err() != nil:
			return printer.Result{}, ctx.Err()
		case errors.As(err, &se):
			return printer.Result{}, printer.Errorf("%s turned down the request: %s", who, proto.Refusal(se.Status, se.Message))
		}
		return printer.Result{}, printer.Errorf("Could not reach %s at %s (%v). Make sure it is on and connected to the same network as this Mac.", who, ep.Authority(), err)
	}

	pc.Report(0.05, "Laying out pages")
	plan, err := PlanJobs(p, pc.Model, pc.Stock, labels, pc.Settings)
	if err != nil {
		return printer.Result{}, err
	}
	details := map[string]any{
		"format": plan.DocumentFormat,
		"dpi":    plan.DPI,
		"media":  plan.Choice.Media.Describe(),
		"exact":  plan.Choice.Exact,
		"jobs":   []int{},
	}
	var note string
	if !plan.Choice.Exact {
		note = fmt.Sprintf(" Each label went at actual size in the middle of a %s page.", plan.Choice.Media.Describe())
	}

	var reported, dropped []string
	if p.Supports(proto.OpValidateJob) && page.Flag(pc.Settings, "validate", true) {
		pc.Report(0.1, "Checking the job with "+who)
		if reported, dropped, err = validate(ctx, client, plan, who, pc.Stock); err != nil {
			return printer.Result{}, err
		}
		details["validated"] = true
		if len(reported) > 0 {
			details["unsupported"] = reported
		}
	}
	if dryRun {
		how := "prepared; the printer cannot check jobs"
		if details["validated"] == true {
			how = "checked by the printer"
		}
		return printer.Result{OK: true, Message: fmt.Sprintf("Dry run: %s, %s. Nothing was printed.", plan.summary(), how), Details: details}, nil
	}
	if len(dropped) > 0 {
		note += fmt.Sprintf(" Left out settings the printer would ignore: %s.", strings.Join(dropped, ", "))
	}
	if slices.Contains(reported, "media-col") {
		note += " The printer warned it may not use exactly the paper asked for (media-col)."
	}
	return d.run(ctx, pc, client, plan, who, note, details)
}

// validate checks the job with Validate-Job. Every job of a plan carries the same
// attributes, so one check covers them all. It returns every attribute the printer
// listed as unsupported, and those it dropped: they are taken out of the plan and the
// job is checked once more without them. media-col is never dropped, because it carries
// the page size (and a roll's cut length) the pages were drawn for; an unsupported
// media-col stops a sheet stock, whose labels would not line up with other paper, and
// is only reported for any other stock.
func validate(ctx context.Context, client *proto.Client, plan *Plan, who string, s *printer.Stock) (reported, dropped []string, err error) {
	for range 2 {
		resp, err := client.ValidateJob(ctx, plan.Jobs[0].Request)
		if err != nil {
			var se *proto.StatusError
			switch {
			case ctx.Err() != nil:
				return nil, nil, ctx.Err()
			case errors.As(err, &se):
				return nil, nil, printer.Errorf("%s turned the job down: %s", who, proto.Refusal(se.Status, se.Message))
			}
			return nil, nil, printer.Errorf("Lost contact with %s while it was checking the job (%v)", who, err)
		}
		unsupported := proto.UnsupportedAttributes(resp)
		if len(unsupported) == 0 {
			break
		}
		if slices.Contains(unsupported, "media-col") && s.Sheet != nil {
			return nil, nil, printer.Errorf("%s would not print on %s for %s, so the labels would not line up with the sheet. Check the paper size set for the tray.",
				who, plan.Choice.Media.Describe(), s.Name)
		}
		var drop []string
		for _, name := range unsupported {
			if !slices.Contains(reported, name) {
				reported = append(reported, name)
			}
			if name != "media-col" && !slices.Contains(dropped, name) {
				drop = append(drop, name)
				dropped = append(dropped, name)
			}
		}
		if len(drop) == 0 {
			break
		}
		plan.SetAttributes(proto.Without(plan.Jobs[0].Request.JobAttributes, drop))
	}
	return reported, dropped, nil
}

// run prints each job and watches it to the end.
func (d *Driver) run(ctx context.Context, pc *printer.PrintContext, client *proto.Client, plan *Plan, who, note string, details map[string]any) (printer.Result, error) {
	poll := d.pollInterval(pc.Settings)
	patience := jobPatience
	if d.patience > 0 {
		patience = d.patience
	}
	total := 0
	for _, j := range plan.Jobs {
		total += j.Labels()
	}
	printed := 0
	var jobIDs, unconfirmed []int
	for i, job := range plan.Jobs {
		part := fmt.Sprintf("job %d of %d", i+1, len(plan.Jobs))
		pc.Report(float64(printed)/float64(total), fmt.Sprintf("Sending %s to %s", part, who))
		id, err := client.PrintJob(ctx, job.Request, job.Document)
		if err != nil {
			var se *proto.StatusError
			switch {
			case ctx.Err() != nil:
				return partial(printed, details), ctx.Err()
			case errors.As(err, &se):
				return failed(who, proto.Refusal(se.Status, se.Message), printed, details)
			case printed == 0:
				return printer.Result{}, printer.Errorf("%s did not take the job (%v)", who, err)
			}
			return failed(who, proto.LostContact, printed, details)
		}
		jobIDs = append(jobIDs, id)
		details["jobs"] = jobIDs
		st, err := client.Watch(ctx, id, proto.Watch{
			Every:    poll,
			Patience: patience,
			OnStatus: func(s proto.JobStatus) {
				done := float64(printed+job.labelsInFirst(s.ImpressionsCompleted)) / float64(total)
				if !s.Stopped() {
					pc.Report(done, fmt.Sprintf("%s on %s: %s", part, who, proto.JobStateName(s.State)))
					return
				}
				pc.Report(done, fmt.Sprintf("Waiting on %s: %s. The job resumes once that is sorted out.", who, stoppedBecause(ctx, client)))
			},
			OnError: func(error) {
				pc.Report(float64(printed)/float64(total), fmt.Sprintf("%s did not answer about %s; asking again", who, part))
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				cancelJob(ctx, client, id, details)
				return partial(printed, details), ctx.Err()
			}
			// The printer took the job before contact was lost, so count it.
			printed += job.Labels()
			pc.Report(float64(printed)/float64(total), fmt.Sprintf("Sent %d of %d", printed, total))
			return failed(who, proto.LostContact, printed, details)
		}
		if st.Gone || !st.Terminal() || st.State == proto.JobCompleted {
			if st.State != proto.JobCompleted || st.Gone {
				unconfirmed = append(unconfirmed, id)
				details["unconfirmed_jobs"] = unconfirmed
			}
			printed += job.Labels()
			pc.Report(float64(printed)/float64(total), fmt.Sprintf("Printed %d of %d", printed, total))
			continue
		}
		pages := max(0, min(st.ImpressionsCompleted, len(job.Pages)))
		status, _ := client.PrinterStatus(ctx)
		details["job_state"] = st.State
		details["printer_state_reasons"] = status.Reasons
		return failed(who, proto.JobOutcome(st, status.State, status.Reasons), printed+job.labelsInFirst(pages), details)
	}
	return printer.Result{OK: true, Printed: printed, Message: fmt.Sprintf("Printed %d label(s) on %s.%s", printed, who, note), Details: details}, nil
}

// stoppedBecause asks the printer why it stopped, for a progress message.
func stoppedBecause(ctx context.Context, client *proto.Client) string {
	status, err := client.PrinterStatus(ctx)
	if err != nil {
		return "it has stopped"
	}
	if why := status.Problem(); why != "" {
		return why
	}
	if status.Message != "" {
		return status.Message
	}
	return "it has stopped"
}

// cancelJob cancels the job on the printer after the caller gave up. ctx has already
// ended, so it uses a short context of its own.
func cancelJob(ctx context.Context, client *proto.Client, id int, details map[string]any) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	if err := client.CancelJob(cctx, id); err != nil {
		details["cancel_error"] = err.Error()
		return
	}
	details["cancelled_job"] = id
}

func partial(printed int, details map[string]any) printer.Result {
	return printer.Result{Printed: printed, Message: "cancelled", Details: details}
}

func failed(who, why string, printed int, details map[string]any) (printer.Result, error) {
	msg := fmt.Sprintf("%s stopped before finishing: %s", who, why)
	if printed > 0 {
		msg = fmt.Sprintf("%s stopped after %d label(s): %s", who, printed, why)
	}
	return printer.Result{Printed: printed, Message: msg, Details: details}, printer.Errorf("%s", msg)
}
