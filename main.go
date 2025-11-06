package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh" // Charmbracelet ssh~
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	wishbubble "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

// Server chat states

type client struct {
	name string
	out  chan string
}

var (
	mu      sync.Mutex
	clients = map[string]*client{}

	joinMsg  = "* %s joined (%d online)"
	leaveMsg = "* %s left (%d online)"

	bus = make(chan string, 256) // broadcast stuff.
)

func init() {
	go func() {
		for msg := range bus {
			mu.Lock()
			for _, c := range clients {
				select {
				case c.out <- msg:
				default:
				}
			}
			mu.Unlock()
		}
	}()
}

func register(c *client) {
	mu.Lock()
	clients[c.name] = c
	n := len(clients)
	mu.Unlock()
	bus <- fmt.Sprintf(joinMsg, c.name, n)
}

func unregister(c *client) {
	mu.Lock()
	if _, ok := clients[c.name]; ok {
		delete(clients, c.name)
	}
	n := len(clients)
	mu.Unlock()
	close(c.out)
	bus <- fmt.Sprintf(leaveMsg, c.name, n)
}

func listNames() []string {
	mu.Lock()
	defer mu.Unlock()
	names := make([]string, 0, len(clients))
	for k := range clients {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Allow for anons too!
func uniqueName(base string) string {
	name := strings.TrimSpace(base)
	if name == "" {
		name = "anon"
	}
	i := 1
	mu.Lock()
	defer mu.Unlock()
	for {
		if _, ok := clients[name]; !ok {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
		i++
	}
}

// styles assign a color per user.

var sysStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

// Nice readable 256-color codes
var palette = []string{
	"39", "45", "51", "69", "75", "81",
	"99", "105", "111", "147", "141", "135",
	"118", "154", "192", "177",
}

func nameStyleFor(user string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = h.Write([]byte(user))
	idx := int(h.Sum32() % uint32(len(palette)))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(palette[idx])).Bold(true)
}

// TUI - Terminal User Interface

type model struct {
	username string
	cl       *client

	vp       viewport.Model
	input    textinput.Model
	help     string
	quitting bool

	logBuf     []string
	nameStyled string
}

type msgIncoming string

func newModel(username string) *model {
	ti := textinput.New()
	ti.Placeholder = "Type message. Use | to send multiple lines. /help for commands."
	ti.Prompt = "› "
	ti.Focus()
	ti.CharLimit = 0 // have to set 0 for no limits. plus ultra.

	vp := viewport.New(80, 20)
	vp.SetContent("")

	cl := &client{name: username, out: make(chan string, 256)}
	nameStyled := nameStyleFor(username).Render(username)

	m := &model{
		username:   username,
		cl:         cl,
		vp:         vp,
		input:      ti,
		help:       "[Enter] send  •  /nick, /list, /help  •  Tip: separate lines with |",
		logBuf:     make([]string, 0, 256),
		nameStyled: nameStyled,
	}
	// Local banner, can modify too based on client ig.
	m.appendBlock(banner())
	return m
}

func (m *model) Init() tea.Cmd {
	register(m.cl)
	return waitIncoming(m.cl.out)
}

func waitIncoming(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		if msg, ok := <-ch; ok {
			return msgIncoming(msg)
		}
		return tea.Quit()
	}
}

func (m *model) appendBlock(block string) {
	m.logBuf = append(m.logBuf, block)
	if len(m.logBuf) > 1000 {
		m.logBuf = m.logBuf[len(m.logBuf)-1000:]
	}
	m.vp.SetContent(strings.Join(m.logBuf, "\n"))
	m.vp.GotoBottom()
}

func (m *model) View() string {
	header := fmt.Sprintf("TRASH GANG — %d online\n", len(listNames()))
	footer := "\n" + m.help + "\n"
	return strings.Join([]string{
		header,
		m.vp.View(),
		footer,
		m.input.View(),
	}, "\n")
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case msgIncoming:
		line := string(msg)
		// Gray broadcasted simple lines
		if strings.HasPrefix(line, "* ") {
			m.appendBlock(sysStyle.Render(line))
		} else {
			m.appendBlock(line)
		}
		return m, waitIncoming(m.cl.out)

	case tea.WindowSizeMsg:
		h := msg.Height - 6
		if h < 5 {
			h = 5
		}
		m.vp.Width = msg.Width
		m.vp.Height = h
		m.input.Width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			unregister(m.cl)
			return m, tea.Quit

		case "enter":
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				return m, nil
			}
			// Commands
			if strings.HasPrefix(raw, "/") {
				if quit := m.runCmd(raw); quit {
					m.quitting = true
					unregister(m.cl)
					return m, tea.Quit
				}
			} else {
				// CHANGE THIS LATER -> Allow for a /ascii command.
				// split on '|' separators -> Mainly for ASCII
				parts := splitSpam(raw)
				for _, p := range parts {
					if p == "" {
						continue
					}
					bus <- fmt.Sprintf("[%s] %s", m.nameStyled, p)
				}
			}
			m.input.SetValue("")
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func splitSpam(s string) []string {
	s = strings.ReplaceAll(s, "\\n", "|")
	chunks := strings.Split(s, "|")
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		c = strings.TrimSpace(c)
		out = append(out, c)
	}
	return out
}

func (m *model) runCmd(line string) (quit bool) {
	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/help":
		m.appendBlock(sysStyle.Render("* Commands: /help /list /nick <name> /quit"))
		m.appendBlock(sysStyle.Render("* Tip: Use `|` between phrases to send multiple lines at once."))
	case "/list":
		m.appendBlock(sysStyle.Render("* users: " + strings.Join(listNames(), ", ")))
	case "/nick":
		if len(parts) < 2 {
			m.appendBlock(sysStyle.Render("* usage: /nick <newname>"))
			break
		}
		newName := uniqueName(strings.TrimSpace(parts[1]))
		mu.Lock()
		if _, ok := clients[m.cl.name]; ok {
			delete(clients, m.cl.name)
		}
		old := m.cl.name
		m.cl.name = newName
		clients[newName] = m.cl
		mu.Unlock()

		m.username = newName
		m.nameStyled = nameStyleFor(newName).Render(newName)
		bus <- fmt.Sprintf("* %s is now known as %s", old, newName)
	case "/quit", "/exit":
		return true
	default:
		m.appendBlock(sysStyle.Render("* Unknown command. Try /help"))
	}
	return false
}

// BubbleTea/Wish integration

func teaHandler(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
	user := strings.TrimSpace(sess.User())
	md := newModel(uniqueName(user))
	opts := wishbubble.MakeOptions(sess)
	return md, opts
}

// Mian

func main() {
	addr := getEnv("ADDR", ":2022")
	keyPath := getEnv("HOSTKEY", "./.trashgang_keys/ed25519")

	if _, err := os.Stat(keyPath); err != nil {
		log.Printf("Host key not found at %s.\nGenerate with:\n  ssh-keygen -t ed25519 -f %s -N ''", keyPath, keyPath)
	}

	s, err := wish.NewServer(
		wish.WithAddress(addr),
		wish.WithHostKeyPath(keyPath),
		wish.WithMiddleware(
			wishbubble.Middleware(teaHandler),
			logging.Middleware(),
			activeterm.Middleware(),
		),
	)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("TrashGang (Wish) listening on %s (SSH)", addr)
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Banner!

func banner() string {
	return `
████████╗██████╗  █████╗ ███████╗██╗  ██╗      ██████╗  █████╗ ███╗   ██╗ ██████╗
╚══██╔══╝██╔══██╗██╔══██╗██╔════╝██║  ██║     ██╔════╝ ██╔══██╗████╗  ██║██╔════╝
   ██║   ██████╔╝███████║███████╗███████║     ██║  ███╗███████║██╔██╗ ██║██║  ███╗
   ██║   ██╔══██╗██╔══██║╚════██║██╔══██║     ██║   ██║██╔══██║██║╚██╗██║██║   ██║
   ██║   ██║  ██║██║  ██║███████║██║  ██║     ╚██████╔╝██║  ██║██║ ╚████║╚██████╔╝
   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝      ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝ ╚═════╝

Welcome! Type /help for commands.
`
}
