package watch

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	pulseGlyph  = "●"
	pulseFor    = 1500 * time.Millisecond
	replyFor    = 2 * time.Second
	fadeAfter   = 5 * time.Second
	removeAfter = 10 * time.Second
	boxWidth    = 46
	maxBoxWidth = 96
	boxGap      = 2
)

var spinner = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// View keeps what the animation needs between ticks: when each task appeared,
// when it last changed, when it finished, and the last command it was seen
// running, so a fading box keeps its text after the service forgets it.
type View struct {
	snap       Snapshot
	start      time.Time
	seen       map[string]time.Time
	changed    map[string]time.Time
	prevStatus map[string]string
	doneAt     map[string]time.Time
	lastLine   map[string]Command
	replyAt    time.Time
	reportAt   time.Time
	lastQueued int
	primed     bool
	// recent holds finished tasks that no longer have a box, newest first.
	recent []Task
}

func NewView(now time.Time) *View {
	return &View{start: now, seen: map[string]time.Time{}, changed: map[string]time.Time{}, prevStatus: map[string]string{}, doneAt: map[string]time.Time{}, lastLine: map[string]Command{}}
}

// Update folds a fresh snapshot into the animation state.
func (v *View) Update(s Snapshot, now time.Time) {
	for _, t := range s.Tasks {
		if _, ok := v.seen[t.ID]; !ok {
			v.seen[t.ID] = now
			// A pulse marks a change we watched happen; the first frame saw
			// nothing happen yet.
			if v.primed {
				v.changed[t.ID] = now
			}
		}
		key := t.Status + "/" + t.Outcome
		if prev, ok := v.prevStatus[t.ID]; ok && prev != key {
			v.changed[t.ID] = now
			// A Worker reaching done or waiting means it reported to the Router.
			if finished(t.Status) || t.Status == "waiting" {
				v.reportAt = now
			}
		}
		v.prevStatus[t.ID] = key
		if finished(t.Status) {
			if _, ok := v.doneAt[t.ID]; !ok {
				switch {
				case !t.EndedAt.IsZero():
					v.doneAt[t.ID] = t.EndedAt
				case v.primed:
					v.doneAt[t.ID] = now
				default:
					// Finished before watching began, with no end time: history.
					v.doneAt[t.ID] = now.Add(-removeAfter)
				}
			}
		} else {
			delete(v.doneAt, t.ID)
		}
	}
	for _, c := range s.Commands {
		if c.Line != "" {
			v.lastLine[c.Work] = c
		}
	}
	if s.QueuedReplies > v.lastQueued {
		v.replyAt = now
	}
	v.lastQueued = s.QueuedReplies
	v.primed = true
	// Finished tasks leave the tree ten seconds after they finished and move
	// to the recent list.
	v.recent = v.recent[:0]
	s.Tasks = slices.DeleteFunc(s.Tasks, func(t Task) bool {
		done, ok := v.doneAt[t.ID]
		if ok && now.Sub(done) >= removeAfter {
			v.recent = append(v.recent, t)
			return true
		}
		return false
	})
	slices.SortFunc(v.recent, func(a, b Task) int {
		switch {
		case a.EndedAt.IsZero() && b.EndedAt.IsZero():
			return strings.Compare(b.ID, a.ID)
		case a.EndedAt.IsZero():
			return 1
		case b.EndedAt.IsZero():
			return -1
		}
		return b.EndedAt.Compare(a.EndedAt)
	})
	v.snap = s
}

type box struct {
	task    Task
	command *Command
	tint    int
	state   string
}

func (v *View) boxes(now time.Time) []box {
	tasks := slices.Clone(v.snap.Tasks)
	slices.SortFunc(tasks, func(a, b Task) int { return v.seen[b.ID].Compare(v.seen[a.ID]) })
	var out []box
	for _, t := range tasks {
		b := box{task: t}
		// The newest command of this task, falling back to the last line seen.
		for i := len(v.snap.Commands) - 1; i >= 0; i-- {
			if v.snap.Commands[i].Work == t.ID {
				c := v.snap.Commands[i]
				if c.Line == "" {
					if last, ok := v.lastLine[t.ID]; ok && last.ID == c.ID {
						c.Line, c.CreatedAt = last.Line, last.CreatedAt
					}
				}
				b.command = &c
				break
			}
		}
		switch {
		case finished(t.Status) && now.Sub(v.doneAt[t.ID]) >= fadeAfter:
			b.tint, b.state = cFaded, t.Status
		case t.Status == "done":
			b.tint, b.state = cDone, "resolved"
		case finished(t.Status):
			b.tint, b.state = cFailed, t.Status
		case t.Status == "waiting":
			b.tint, b.state = cWaiting, "waiting"
		default:
			b.tint, b.state = cRunning, "running"
		}
		out = append(out, b)
	}
	return out
}

// Render draws one frame of exactly height lines, each at most cols cells.
func (v *View) Render(now time.Time, cols, height int) []string {
	if cols < 20 {
		cols = 20
	}
	ms := now.Sub(v.start).Milliseconds()
	lines := v.header(now, ms, cols)
	boxes := v.boxes(now)
	if len(boxes) == 0 {
		lines = append(lines, v.idle(ms, cols)...)
		lines = append(lines, v.recentLines(now, cols, 5)...)
	} else {
		lines = append(lines, v.tree(boxes, now, ms, cols)...)
		lines = append(lines, v.recentLines(now, cols, 3)...)
	}
	footer := color(cDim, fit(fmt.Sprintf(" q quit · workers %d · running %d · %s", len(boxes), v.running(), now.Format("15:04:05")), cols))
	body := height - 1
	if body < 1 {
		return []string{clip(footer, cols)}
	}
	if len(lines) > body {
		lines = lines[:body]
	}
	for len(lines) < body {
		lines = append(lines, "")
	}
	lines = append(lines, footer)
	for i := range lines {
		lines[i] = clip(lines[i], cols)
	}
	return lines
}

// tags lists the threads the Router serves, one per source and channel.
func (v *View) tags() []string {
	var tags []string
	for _, t := range v.snap.Threads {
		if tag := shortThread(t); !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (v *View) running() int {
	n := 0
	for _, c := range v.snap.Commands {
		if !c.Finished {
			n++
		}
	}
	return n
}

func (v *View) header(now time.Time, ms int64, cols int) []string {
	glyph := "◉"
	if (ms/600)%2 == 1 {
		glyph = "◎"
	}
	router := color(cRouter, bold(glyph+" ROUTER"))
	if now.Sub(v.reportAt) < replyFor && !v.reportAt.IsZero() {
		router += " " + invert(color(cRouter, " ↤ report "))
	}
	if now.Sub(v.replyAt) < replyFor && !v.replyAt.IsZero() {
		router += " " + invert(color(cRouter, " ↦ reply "))
	} else if v.snap.QueuedReplies > 0 {
		router += color(cRouter, fmt.Sprintf(" ↦ %d queued", v.snap.QueuedReplies))
	}
	if v.snap.FailedReplies > 0 {
		router += color(cFailed, fmt.Sprintf(" ✗ %d failed", v.snap.FailedReplies))
	}
	if v.snap.Timers > 0 {
		router += color(cText, fmt.Sprintf(" ⏲ %d", v.snap.Timers))
	}
	threads := strings.Join(v.tags(), "  ")
	heart := "♥"
	if (ms/800)%2 == 1 {
		heart = "♡"
	}
	beam := []rune("············")
	pos := int((ms / 120) % int64(len(beam)+4))
	for i, r := range []rune("░▒▓") {
		if p := pos - 2 + i; p >= 0 && p < len(beam) {
			beam[p] = r
		}
	}
	watchdog := color(cWatchdog, "◎ WATCHDOG "+heart+" "+string(beam))
	left := " " + router
	right := watchdog + " "
	gap := cols - width_(left) - width_(right)
	line1 := left + strings.Repeat(" ", max(1, gap)) + right
	line2 := color(cDim, " │ ") + color(cText, ellipsis(threads, cols-4))
	return []string{line1, line2}
}

func width_(s string) int { return width(plain(s)) }

func (v *View) idle(ms int64, cols int) []string {
	dots := strings.Repeat("·", int((ms/700)%4))
	listening := "nothing to listen on"
	if len(v.snap.Threads) > 0 {
		listening = "listening on " + strings.Join(v.tags(), ", ")
	}
	text := "◌ nothing running · " + listening + " " + dots
	pad := max(0, (cols-width(text))/2)
	return []string{color(cDim, " │"), "", strings.Repeat(" ", pad) + color(cDim, text)}
}

// recentLines lists finished work that has left the tree, so an idle screen
// still says what Golem did last.
func (v *View) recentLines(now time.Time, cols, limit int) []string {
	if len(v.recent) == 0 {
		return nil
	}
	lines := []string{"", color(cDim, "   recently finished")}
	for _, t := range v.recent[:min(limit, len(v.recent))] {
		mark, tint, label := "✓", cDone, t.Outcome
		if t.Status != "done" {
			mark, tint, label = "✗", cFailed, t.Status
		}
		thread := ""
		if len(t.Threads) > 0 {
			thread = " · " + shortThread(t.Threads[0])
		}
		line := "   " + color(tint, mark) + " " + color(cText, ellipsis(t.ID+"  "+clean(t.Title), max(10, cols/2))) + color(cDim, " · "+label+" · "+ago(now, t.EndedAt)+thread)
		lines = append(lines, clip(line, cols))
	}
	return lines
}

// short prints a duration as minutes, hours or days.
func short(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ago says how long ago a task ended, coarsely.
func ago(now, at time.Time) string {
	if at.IsZero() {
		return "earlier"
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// tree lays the boxes out in rows under the Router, each row hung from a bus
// that the trunk at the left margin feeds.
func (v *View) tree(boxes []box, now time.Time, ms int64, cols int) []string {
	// Rows pack boxes at their minimum width, then each row's boxes grow to
	// share the terminal's width, up to maxBoxWidth, so a lone box can show a
	// whole command or question on a wide screen.
	minBox := min(boxWidth, cols-4)
	perRow := max(1, (cols-3)/(minBox+boxGap))
	var lines []string
	for row := 0; row*perRow < len(boxes); row++ {
		rowBoxes := boxes[row*perRow : min(len(boxes), (row+1)*perRow)]
		n := len(rowBoxes)
		bw := max(minBox, min(maxBoxWidth, (cols-3-(n-1)*boxGap)/n))
		lines = append(lines, v.connectors(rowBoxes, now, bw, row == 0)...)
		rendered := make([][]string, len(rowBoxes))
		for i, b := range rowBoxes {
			rendered[i] = v.box(b, now, ms, bw)
		}
		for line := range 5 {
			var parts []string
			for i := range rowBoxes {
				parts = append(parts, rendered[i][line])
			}
			lines = append(lines, color(cDim, " │ ")+strings.Join(parts, strings.Repeat(" ", boxGap)))
		}
	}
	lines = append(lines, color(cDim, " ╵"))
	return lines
}

// connectors draws the bus and the drops to a row of boxes. A pulse marks a
// box whose task just appeared (travelling down) or just reported (up).
func (v *View) connectors(rowBoxes []box, now time.Time, bw int, first bool) []string {
	bus := make([]rune, 3+len(rowBoxes)*(bw+boxGap))
	drop := make([]rune, len(bus))
	for i := range bus {
		bus[i], drop[i] = ' ', ' '
	}
	bus[1] = '│'
	drop[1] = '│'
	if first {
		bus[1] = '├'
	} else {
		bus[1] = '├'
	}
	centers := make([]int, len(rowBoxes))
	for i := range rowBoxes {
		centers[i] = 3 + i*(bw+boxGap) + bw/2
	}
	for c := 2; c <= centers[len(centers)-1]; c++ {
		bus[c] = '─'
	}
	for i, c := range centers {
		switch {
		case len(centers) == 1 || i == len(centers)-1:
			bus[c] = '┐'
		default:
			bus[c] = '┬'
		}
		drop[c] = '▼'
	}
	var pulses []string
	for i, b := range rowBoxes {
		changed, ok := v.changed[b.task.ID]
		age := now.Sub(changed)
		if ok && age < pulseFor && age >= 0 {
			if finished(b.task.Status) || b.task.Status == "waiting" {
				drop[centers[i]] = '▲'
			} else {
				drop[centers[i]] = []rune(pulseGlyph)[0]
			}
			pulses = append(pulses, string(drop[centers[i]]))
		}
	}
	busLine := color(cDim, string(bus))
	dropLine := color(cDim, string(drop))
	for _, p := range pulses {
		dropLine = strings.Replace(dropLine, p, color(cBright, bold(p))+"\x1b[38;5;"+itoa(cDim)+"m", 1)
	}
	return []string{busLine, dropLine}
}

func (v *View) box(b box, now time.Time, ms int64, bw int) []string {
	inner := bw - 2
	tint := func(s string) string { return color(b.tint, s) }
	right := " " + b.state + " "
	left := " " + ellipsis(b.task.ID+"  "+clean(b.task.Title), max(4, inner-width(right)-1)) + " "
	top := clip("┌"+left+strings.Repeat("─", max(0, inner-width(left)-width(right)))+right+"┐", bw)
	thread := ""
	if len(b.task.Threads) > 0 {
		thread = shortThread(b.task.Threads[0])
	}
	var line2, line3 string
	switch {
	case b.task.Status == "waiting":
		// The question is what matters here, so it flows across both lines.
		head := " ⧖ " + clean(b.task.NextActor)
		if !b.task.WaitingAt.IsZero() {
			head += " · " + short(now.Sub(b.task.WaitingAt))
		}
		head += ": "
		first, rest := wrap(clean(b.task.NextAction), inner-width(head))
		line2 = head + first
		line3 = ellipsis("   "+rest, inner)
	case b.command == nil && finished(b.task.Status):
		line2 = ellipsis(" ✓ "+firstLine(b.task.Summary), inner)
		line3 = ""
	case b.command == nil:
		line2 = " ⟩ " + strings.Repeat("·", 1+int((ms/400)%3)) + " deciding"
		line3 = ""
	case b.command.Finished:
		line2 = ellipsis(" ⟩ "+clean(b.command.Line), inner)
		if finished(b.task.Status) && b.task.Summary != "" {
			line2 = ellipsis(" ✓ "+firstLine(b.task.Summary), inner)
		}
		mark, verdict := "✓", "done"
		if b.command.Status != "done" || b.command.ExitCode != 0 {
			mark, verdict = "✗", b.command.Status
		}
		line3 = ellipsis(fmt.Sprintf(" %s %s exit %d  %s", mark, verdict, b.command.ExitCode, shortJob(b.command.ID)), inner)
	default:
		line2 = ellipsis(" ⟩ "+clean(b.command.Line), inner)
		job := shortJob(b.command.ID)
		elapsed := fmt.Sprintf("%02d:%02d", int(now.Sub(b.command.CreatedAt).Minutes()), int(now.Sub(b.command.CreatedAt).Seconds())%60)
		barWidth := inner - 4 - width(elapsed) - width(job) - 2
		line3 = " " + spinner[(ms/100)%int64(len(spinner))] + " " + pipeline(barWidth, ms) + " " + elapsed + " " + job
	}
	status := " " + thread + " · " + b.task.Outcome
	if b.task.Environment != "" && b.task.Environment != "local" {
		status += " · " + b.task.Environment
	}
	if b.task.Cost > 0 {
		status += fmt.Sprintf(" · $%.2f", b.task.Cost)
	}
	line4 := ellipsis(status, inner)
	bottom := "└" + strings.Repeat("─", inner) + "┘"
	frame := func(s string) string { return tint("│") + color(cText, fit(s, inner)) + tint("│") }
	return []string{tint(top), frame(line2), frame(line3), frame(color(cDim, line4)), tint(bottom)}
}

// pipeline is the bar with chevrons travelling along it while a command runs.
func pipeline(w int, ms int64) string {
	if w < 4 {
		return strings.Repeat("═", max(0, w))
	}
	bar := []rune(strings.Repeat("═", w))
	n := max(1, w/7)
	stride := w / n
	for k := range n {
		p := int((ms/80 + int64(k*stride)) % int64(w))
		bar[p] = '▸'
		if p > 0 {
			bar[p-1] = '▹'
		}
	}
	return color(cRunning, string(bar))
}

// clean makes a command line safe for one terminal row: a newline becomes a
// visible mark and other control characters vanish.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune('⏎')
		case r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wrap splits s at a word boundary so the first part fits in w cells; the
// rest is returned for the next line.
func wrap(s string, w int) (string, string) {
	if width(s) <= w {
		return s, ""
	}
	if w < 1 {
		return "", s
	}
	runes := []rune(s)
	// Walk to the last rune that still fits in w cells, then back to a space.
	fitEnd, used := 0, 0
	for fitEnd < len(runes) && used+cells(runes[fitEnd]) <= w {
		used += cells(runes[fitEnd])
		fitEnd++
	}
	cut := fitEnd
	for i := fitEnd; i > fitEnd/2; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return string(runes[:cut]), strings.TrimLeft(string(runes[cut:]), " ")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return clean(line)
}

func shortJob(id string) string {
	if len(id) <= 16 {
		return id
	}
	if i := strings.LastIndex(id, "_"); i > 0 {
		return "job_…" + id[i:]
	}
	return id[:8] + "…"
}

// shortThread keeps the source and the channel, which identify the thread to
// a person, and drops the Slack message timestamp.
func shortThread(t string) string {
	if parts := strings.SplitN(t, ":", 3); len(parts) == 3 {
		return parts[0] + ":" + parts[1]
	}
	return t
}
