// edgelab-tui is the v3 stage-5 admin console: a small Bubble Tea live view
// that polls the edgelab admin unix socket (~1s) and renders hub/origin stats,
// registry watch status and the durable event tail. It observes; it never
// changes delivery semantics.
//
// Modes:
//
//	edgelab-tui --socket work/edge/admin.sock          live dashboard (TTY)
//	edgelab-tui --socket ... --json stats              one NDJSON request
//	edgelab-tui --socket ... --csv watch-status        one request as CSV
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"example.com/edge-delta-lab/internal/admin"
)

type statsTickMsg struct {
	stats      json.RawMessage
	watch      []admin.WatchStatusEntry
	events     []admin.Event
	oldest     int64
	latest     int64
	adminConns int
	err        error
}

// call issues one NDJSON request over the admin unix socket and returns the
// raw result. It is shared by the TUI poller and the one-shot --json mode.
func call(socket, method string, params string) (json.RawMessage, error) {
	if params == "" {
		params = "{}"
	}
	c, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := fmt.Sprintf(`{"id":1,"method":%q,"params":%s}`+"\n", method, params)
	if _, err := c.Write([]byte(req)); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, fmt.Errorf("bad admin response: %w", err)
	}
	if !resp.OK {
		msg := "unknown error"
		if resp.Error != nil {
			msg = resp.Error.Code + ": " + resp.Error.Message
		}
		return nil, fmt.Errorf("admin %s: %s", method, msg)
	}
	return resp.Result, nil
}

// poll gathers one full dashboard snapshot. Any single failing method fails
// the tick; the view keeps the previous frame and shows the error.
func poll(socket string) tea.Cmd {
	return func() tea.Msg {
		m := statsTickMsg{}
		raw, err := call(socket, "stats", "")
		if err != nil {
			m.err = err
			return m
		}
		m.stats = raw
		if raw, err = call(socket, "watch-status", ""); err == nil {
			var wr struct {
				Watchers []admin.WatchStatusEntry `json:"watchers"`
			}
			if json.Unmarshal(raw, &wr) == nil {
				m.watch = wr.Watchers
			}
		}
		if raw, err = call(socket, "events", `{"after":0,"limit":12}`); err == nil {
			var er struct {
				Events []admin.Event `json:"events"`
				Oldest int64         `json:"oldest"`
				Latest int64         `json:"latest"`
			}
			if json.Unmarshal(raw, &er) == nil {
				m.events, m.oldest, m.latest = er.Events, er.Oldest, er.Latest
			}
		}
		if raw, err = call(socket, "clients", ""); err == nil {
			var cr struct {
				AdminConnections int `json:"admin_connections"`
			}
			if json.Unmarshal(raw, &cr) == nil {
				m.adminConns = cr.AdminConnections
			}
		}
		return m
	}
}

type model struct {
	socket string
	width  int
	height int
	cur    statsTickMsg
	quitting bool
}

func (m model) Init() tea.Cmd { return poll(m.socket) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch t := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = t.Width, t.Height
		return m, nil
	case tea.KeyMsg:
		if s := t.String(); s == "q" || s == "ctrl+c" || s == "esc" {
			m.quitting = true
			return m, tea.Quit
		}
	case statsTickMsg:
		m.cur = t
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return poll(m.socket)() })
	}
	return m, nil
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > w {
		return string(r[:max(0, w-1)]) + "…"
	}
	return s
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	head := "edgelab-tui · " + m.socket
	if m.cur.err != nil {
		head += " · ERROR: " + m.cur.err.Error()
	} else {
		head += fmt.Sprintf(" · admin_conns=%d · %s", m.cur.adminConns, time.Now().Format("15:04:05"))
	}
	b.WriteString(trunc(head, m.width) + "\n")
	b.WriteString(strings.Repeat("─", max(0, min(m.width, 80))) + "\n")
	b.WriteString("STATS\n")
	if m.cur.stats != nil {
		var pretty map[string]any
		if json.Unmarshal(m.cur.stats, &pretty) == nil {
			for _, k := range []string{"origin", "hub"} {
				if v, ok := pretty[k]; ok {
					line, _ := json.Marshal(v)
					b.WriteString(trunc("  "+k+": "+string(line), m.width) + "\n")
				}
			}
		}
	} else {
		b.WriteString("  (no data yet)\n")
	}
	b.WriteString("\nWATCH STATUS\n")
	if len(m.cur.watch) == 0 {
		b.WriteString("  (no registry watchers)\n")
	}
	for _, w := range m.cur.watch {
		b.WriteString(trunc(fmt.Sprintf("  %s tag=%s digest=%s", w.Repo, w.Tag, shortDigest(w.Digest)), m.width) + "\n")
	}
	b.WriteString("\nEVENTS (tail, oldest retained seq " + fmt.Sprint(m.cur.oldest) + ")\n")
	if len(m.cur.events) == 0 {
		b.WriteString("  (no events)\n")
	}
	for _, e := range m.cur.events {
		b.WriteString(trunc(fmt.Sprintf("  %3d %s %s %s", e.Seq, e.Time[11:19], e.Kind, e.Detail), m.width) + "\n")
	}
	b.WriteString("\n[q] quit · polling every 1s · seq range " +
		fmt.Sprint(m.cur.oldest) + ".." + fmt.Sprint(m.cur.latest))
	return b.String()
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19] + "…"
	}
	return d
}

func runLive(socket string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Static snapshot when stdin is not a TTY (CI, pipes): render one
		// poll result and exit, so scripted use never blocks.
		m := statsTickMsg{}
		msg := poll(socket)()
		if t, ok := msg.(statsTickMsg); ok {
			m = t
		}
		if m.err != nil {
			return m.err
		}
		out, _ := json.MarshalIndent(map[string]any{
			"stats": json.RawMessage(m.stats), "watch": m.watch, "events": m.events,
			"oldest": m.oldest, "latest": m.latest,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	p := tea.NewProgram(model{socket: socket}, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func main() {
	socket := flag.String("socket", "work/edge/admin.sock", "admin unix socket path")
	jsonMethod := flag.String("json", "", "one-shot: send <method> and print the raw NDJSON result")
	csvMethod := flag.String("csv", "", "one-shot: send <method> and print the result as CSV")
	params := flag.String("params", "", "JSON params for --json/--csv (default {})")
	flag.Parse()
	switch {
	case *jsonMethod != "":
		raw, err := call(*socket, *jsonMethod, *params)
		if err != nil {
			fatal(err)
		}
		var pretty bytes.Buffer
		if json.Indent(&pretty, raw, "", "  ") != nil {
			fmt.Println(string(raw))
		} else {
			pretty.WriteTo(os.Stdout)
			fmt.Println()
		}
	case *csvMethod != "":
		raw, err := call(*socket, *csvMethod, *params)
		if err != nil {
			fatal(err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			fatal(err)
		}
		s, err := admin.CSV(v)
		if err != nil {
			fatal(err)
		}
		fmt.Print(s)
	default:
		if err := runLive(*socket); err != nil {
			fatal(err)
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "edgelab-tui:", err)
	os.Exit(1)
}
