// cozy is a tui browser for the small web (gemini, spartan, nex)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"blekksprut.net/cozy"
	"blekksprut.net/natto/gemini"
	"blekksprut.net/natto/spartan"
	"blekksprut.net/yofukashi"
	"blekksprut.net/yofukashi/nex"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Theme struct {
	Text, Background, Status tcell.Color
}

type Request struct {
	rawURL   string
	remember bool
	length   int64
	data     io.Reader
}

type Link struct {
	rawURL string
	upload bool
}

type Visit struct {
	URL    *url.URL
	Scroll int
}

type Cozy struct {
	scheme      string
	scroll      int
	remembering bool
	cancel      context.CancelFunc
	requests    chan *Request
	current     *url.URL
	history     []Visit
	shortcut    bool
	app         *tview.Application
	flex        *tview.Flex
	frame       *tview.Frame
	address     *tview.InputField
	pages       *tview.Pages
	station     *tview.TextView
	theme       *Theme
	themes      map[string]*Theme
	links       map[string]Link
	trusted     map[string]string
}

func NewCozy(theme string) *Cozy {
	c := Cozy{
		app:     tview.NewApplication(),
		flex:    tview.NewFlex().SetDirection(tview.FlexRow),
		address: tview.NewInputField(),
		station: tview.NewTextView(),
		pages:   tview.NewPages(),
	}
	c.station.SetWordWrap(true).SetDynamicColors(true)
	c.requests = make(chan *Request, 1)
	c.themes = make(map[string]*Theme)

	c.SetScheme("gemini")
	c.themes["day"] = &Theme{
		tcell.GetColor("#171717"),
		tcell.GetColor("#ffffff"),
		tcell.GetColor("#05acff"),
	}
	c.themes["night"] = &Theme{
		tcell.GetColor("#fcfcfc"),
		tcell.GetColor("#171717"),
		tcell.GetColor("#a22041"),
	}
	c.frame = tview.NewFrame(c.flex).SetBorders(0, 0, 0, 0, 0, 0)

	switch theme {
	case "":
		c.CheckDaylight()
	case "night":
		c.SetTheme(c.themes["night"])
	case "day":
		c.SetTheme(c.themes["day"])
	default:
		fmt.Fprintln(os.Stderr, "unknown theme")
		os.Exit(1)
	}
	c.pages.AddAndSwitchToPage("station", c.station, true)
	c.frame.Clear()
	c.SetStatus("🍵", "", false)
	c.flex.AddItem(c.address, 1, 1, true)
	c.flex.AddItem(c.pages, 0, 1, false)

	c.address.SetDoneFunc(func(key tcell.Key) {
		if key != tcell.KeyEnter {
			return
		}
		address := c.address.GetText()
		if address == "" {
			c.shortcut = false
			c.address.SetText(c.current.String())
			c.app.SetFocus(c.pages)
			return
		}
		if c.shortcut {
			c.shortcut = false
			c.address.SetText(c.current.String())
			link, ok := c.links[address]
			if ok {
				resolved, err := c.current.Parse(link.rawURL)
				if err != nil {
					c.Error(fmt.Errorf("invalid shortcut"), false)
					return
				}
				if link.upload {
					c.RequestUpload(resolved)
					return
				} else {
					address = resolved.String()
				}
			} else {
				c.Error(fmt.Errorf("shortcut not found"), false)
				return
			}
		}
		c.app.SetFocus(c.pages)

		c.Go(address, true)
	})
	c.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return c.GlobalEvent(event)
	})
	c.pages.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return c.PagesEvent(event)
	})
	c.app.SetRoot(c.frame, true)

	go c.SendRequests()

	return &c
}

func (c *Cozy) Go(rawURL string, remember bool) {
	if c.remembering && len(c.history) > 0 {
		scroll, _ := c.station.GetScrollOffset()
		c.history[len(c.history)-1].Scroll = scroll
		c.remembering = false
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.requests <- &Request{rawURL: rawURL, remember: remember}
}

func (c *Cozy) SendRequests() {
	for {
		select {
		case req := <-c.requests:
			ctx, cancel := context.WithCancel(context.Background())
			c.cancel = cancel
			c.Fetch(ctx, req)
			defer cancel()
		}
	}
}

func (c *Cozy) PagesEvent(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyCtrlO:
		c.Bookmarks()
	case tcell.KeyCtrlY:
		err := c.Bookmark()
		if err != nil {
			c.Error(err, false)
		} else {
			c.SetStatus("🍵", "bookmarked", false)
		}
	case tcell.KeyLeft:
		if event.Modifiers()&tcell.ModShift != 0 {
			c.Back()
		}
	}
	return event
}

func (c *Cozy) GlobalEvent(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyCtrlG:
		c.SetScheme("gemini")
		c.address.SetText("")
		c.app.SetFocus(c.address)
	case tcell.KeyCtrlS:
		c.SetScheme("spartan")
		c.address.SetText("")
		c.app.SetFocus(c.address)
	case tcell.KeyCtrlN:
		c.SetScheme("nex")
		c.address.SetText("")
		c.app.SetFocus(c.address)
	case tcell.KeyCtrlL:
		c.shortcut = true
		c.address.SetText("")
		c.app.SetFocus(c.address)
	case tcell.KeyCtrlH:
		c.ShowFile("/gmi/cozy.gmi")
	case tcell.KeyRune:
		if event.Rune() == '.' && c.cancel != nil {
			c.cancel()
		}
	case tcell.KeyTab, tcell.KeyCtrlE:
		if c.pages.HasFocus() {
			c.app.SetFocus(c.address)
		} else {
			c.app.SetFocus(c.pages)
		}
	case tcell.KeyCtrlR:
		c.Reload()
	case tcell.KeyCtrlRightSq:
		c.app.Sync()
	}
	return event
}

func (c *Cozy) SetTheme(t *Theme) {
	c.theme = t
	c.address.SetFieldBackgroundColor(t.Status)
	c.address.SetFieldTextColor(t.Text)
	style := tcell.Style{}.Background(t.Status).Foreground(t.Text)
	c.address.SetLabelStyle(style)
	c.station.SetBackgroundColor(t.Background)
	c.station.SetTextColor(t.Text)
	c.frame.SetBackgroundColor(t.Status)
}

func (c *Cozy) SetStatus(status string, result string, async bool) {
	f := func() {
		c.frame.Clear()
		status = fmt.Sprintf(" %s cozy %s", status, cozy.Version)
		c.frame.AddText(status, false, tview.AlignLeft, c.theme.Text)
		c.frame.AddText(result+" ", false, tview.AlignRight, c.theme.Text)
	}
	if async {
		c.app.QueueUpdateDraw(f)
	} else {
		f()
	}
}

func (c *Cozy) Back() {
	if len(c.history) > 1 {
		c.history = c.history[:len(c.history)-1]
		visit := c.history[len(c.history)-1]
		c.Go(visit.URL.String(), false)
		c.scroll = visit.Scroll
		c.remembering = true
	}
}

func (c *Cozy) ConfigPath(path string) string {
	config, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config, "cozy", path)
}

func (c *Cozy) InitializeBookmarks() {
	path := c.ConfigPath("bookmarks.gmi")
	err := os.MkdirAll(filepath.Dir(path), 0750)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	fmt.Fprintln(f, "# cozy bookmarks")
	fmt.Fprintln(f)
}

func (c *Cozy) InitializeTrust() {
	path := c.ConfigPath("hosts")
	err := os.MkdirAll(filepath.Dir(path), 0750)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	defer f.Close()
}

func (c *Cozy) Trust(host, signature string) error {
	path := c.ConfigPath("hosts")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, host, signature)
	return err
}

func (c *Cozy) CheckTrust(host, signature string) error {
	path := c.ConfigPath("hosts")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		h, s, found := strings.Cut(scanner.Text(), " ")
		if !found {
			return fmt.Errorf("malformed hosts file")
		}
		if h == host {
			if s != signature {
				return fmt.Errorf("signature doesn't match")
			}
			return nil
		}
	}
	err = c.Trust(host, signature)
	if err != nil {
		return fmt.Errorf("trust on first use failed")
	}
	return nil
}

func (c *Cozy) Bookmark() error {
	path := c.ConfigPath("bookmarks.gmi")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "=> %s\n", c.current.String())
	return err
}

func (c *Cozy) Bookmarks() {
	path := c.ConfigPath("bookmarks.gmi")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	c.station.Clear()
	c.ShowGemtext(f)
	c.address.SetText("file://" + path)
	c.current, _ = url.Parse("bookmarks")
	c.app.SetFocus(c.pages)
}

func (c *Cozy) Reload() {
	if c.current != nil {
		c.Go(c.current.String(), false)
	}
}

func (c *Cozy) Error(err error, async bool) {
	c.SetStatus("🍉", err.Error(), async)
}

func (c *Cozy) Fetch(ctx context.Context, req *Request) {
	c.SetStatus("⏳", "...", true)
	u, err := url.Parse(req.rawURL)
	if err != nil {
		c.Error(err, true)
	}
	if u.Scheme == "" {
		u.Scheme = c.scheme
	}
	u, _ = url.Parse(u.String())

	switch u.Scheme {
	case "gemini":
		c.scheme = "gemini"
		err := c.Gemini(ctx, u)
		if err != nil {
			c.Error(err, true)
		}
	case "spartan":
		c.scheme = "spartan"
		err := c.Spartan(ctx, u, req)
		if err != nil {
			c.Error(err, true)
		}
	case "nex":
		c.scheme = "nex"
		c.Nex(ctx, u)
	case "http", "https", "mailto":
		cmd := exec.Command("open", u.String())
		cmd.Run()
	case "file":
		c.ShowFile(u.Path)
	default:
		c.Error(fmt.Errorf("invalid protocol %s", u.Scheme), true)
	}
	if c.current != nil {
		c.PushURL(c.current, req.remember)
	}
}

func (c *Cozy) PushURL(u *url.URL, remember bool) {
	if remember {
		visit := Visit{URL: c.current}
		c.history = append(c.history, visit)
		c.remembering = true
	}
	c.app.QueueUpdateDraw(func() {
		c.address.SetText(c.current.String())
	})
}

func (c *Cozy) SetScheme(scheme string) {
	c.scheme = scheme
	switch c.scheme {
	case "gemini":
		c.address.SetLabel(" 🚀 ")
	case "spartan":
		c.address.SetLabel(" 💪 ")
	default:
		c.address.SetLabel(" 🌃 ")
	}
}

func (c *Cozy) Nex(ctx context.Context, u *url.URL) {
	rt := time.Now()
	r, err := nex.Request(ctx, u.String())
	if err != nil {
		c.Error(err, true)
		return
	}
	// defer r.Close() - next yofukashi version
	delta := time.Since(rt).Round(time.Millisecond)
	path := u.Path
	switch {
	case strings.HasSuffix(path, ".webp"):
		fallthrough
	case strings.HasSuffix(path, ".gif"):
		fallthrough
	case strings.HasSuffix(path, ".png"):
		fallthrough
	case strings.HasSuffix(path, ".jpg"):
		c.ShowImage(r)
	case path == "", strings.HasSuffix(path, "/"):
		c.ShowGemtext(r)
	default:
		c.ShowText(r)
	}
	c.current = u
	c.SetScheme(c.scheme)
	c.SetStatus("🍵", delta.String(), true)
}

func (c *Cozy) Gemini(ctx context.Context, u *url.URL) error {
	rt := time.Now()
	r, err := gemini.Request(ctx, u.String())
	if err != nil {
		return err
	}
	defer r.Close()

	err = c.CheckTrust(u.Hostname(), r.SignatureBase64())
	if err != nil {
		return err
	}

	switch r.Status / 10 {
	case 1:
		return c.RequestInput(u, r.Header, r.Status == 11)
	case 2:
		delta := time.Since(rt).Round(time.Millisecond)
		return c.Success(r.URL, r, r.Header, delta.String())
	case 4, 5:
		return fmt.Errorf("%d: %s", r.Status, r.Header)
	case 6:
		return fmt.Errorf("auth not supported yet")
	}
	return nil
}

func (c *Cozy) Spartan(ctx context.Context, u *url.URL, req *Request) error {
	rt := time.Now()
	if u.Path == "" {
		u.Path = "/"
	}
	data := spartan.Data{Length: req.length, Data: req.data}
	r, err := spartan.Request(ctx, u.String(), data)
	if err != nil {
		return err
	}
	defer r.Close()

	switch r.Status {
	case spartan.Success:
		delta := time.Since(rt).Round(time.Millisecond)
		return c.Success(r.URL, r, r.Header, delta.String())
	case spartan.ClientError, spartan.ServerError:
		return fmt.Errorf("%d: %s", r.Status, r.Header)
	}
	return nil
}

func (c *Cozy) Success(u *url.URL, r io.Reader, header, d string) error {
	c.station.Clear()
	switch {
	case strings.HasPrefix(header, "text/gemini"):
		c.ShowGemtext(r)
	case strings.HasPrefix(header, "text/"):
		c.ShowText(r)
	case strings.HasPrefix(header, "application/octet-stream"):
		c.ShowText(r)
	case strings.HasPrefix(header, "image/"):
		c.ShowImage(r)
	default:
		return fmt.Errorf("unsupported mime type %s", header)
	}
	c.SetStatus("🍵", d, true)
	c.SetScheme(c.scheme)
	c.current = u
	return nil
}

func (c *Cozy) ShowText(r io.Reader) {
	c.station.Clear()
	io.Copy(c.station, r)
}

func (c *Cozy) ShowFile(raw string) {
	path := strings.TrimPrefix(raw, "/")
	f, err := cozy.FS.Open(path)
	if err != nil {
		return
	}
	c.station.Clear()
	c.ShowGemtext(f)
	c.current, _ = url.Parse(raw)
	c.current.Scheme = "file"
	c.address.SetText(c.current.String())
	c.app.SetFocus(c.pages)
}

func (c *Cozy) ParseLink(line, shortcut string) (string, string) {
	s := regexp.MustCompile("[[:space:]]+").Split(line, 2)
	switch len(s) {
	case 1:
		t := fmt.Sprintf("[deeppink::b]%s[-:-:-]", s[0])
		sct := fmt.Sprintf("[blue::d]%3s[-:-:-]", shortcut)
		return fmt.Sprintf("%s %s", sct, t), s[0]
	default:
		t := fmt.Sprintf("[deeppink::b]%s[-:-:-]", s[1])
		sct := fmt.Sprintf("[steelblue::d]%3s[-:-:-]", shortcut)
		return fmt.Sprintf("%s %s (%s)", sct, t, s[0]), s[0]
	}
}

func (c *Cozy) ShowGemtext(r io.Reader) {
	c.links = make(map[string]Link)
	c.station.Clear()
	c.pages.RemovePage("image")
	c.pages.RemovePage("prompt")
	scanner := bufio.NewScanner(r)
	fmt.Fprintln(c.station)
	var pre bool
	i := 0
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "```"):
			pre = !pre
			continue
		case pre:
			fmt.Fprintf(c.station, "    [::d]%s[-:-:-]\n", line)
		case strings.HasPrefix(line, "=>"):
			line := strings.TrimSpace(strings.TrimPrefix(line, "=>"))
			i++
			shortcut := fmt.Sprintf("%x", i)
			gmi, ln := c.ParseLink(line, shortcut)
			fmt.Fprintln(c.station, gmi)
			c.links[shortcut] = Link{ln, false}
		case c.scheme == "spartan" && strings.HasPrefix(line, "=:"):
			line := strings.TrimSpace(strings.TrimPrefix(line, "=:"))
			i++
			shortcut := fmt.Sprintf("%x", i)
			gmi, ln := c.ParseLink(line, shortcut)
			fmt.Fprintln(c.station, gmi)
			c.links[shortcut] = Link{ln, true}
		case strings.HasPrefix(line, "*"):
			fmt.Fprintf(c.station, "  [green::]%s[-:-:-]\n", line)
		case strings.HasPrefix(line, ">"):
			quote := strings.TrimSpace(strings.TrimPrefix(line, ">"))
			fmt.Fprintf(c.station, "%-4s[steelblue::i]%s[-:-:-]\n", "", quote)
		case strings.HasPrefix(line, "###"):
			fmt.Fprintf(c.station, "[yellow::b]%s[-:-:-]\n", line)
		case strings.HasPrefix(line, "##"):
			fmt.Fprintf(c.station, " [orange::b]%s[-:-:-]\n", line)
		case strings.HasPrefix(line, "#"):
			fmt.Fprintf(c.station, "  [red::b]%s[-:-:-]\n", line)
		default:
			fmt.Fprintln(c.station, "   ", line)
		}
	}
	c.app.SetFocus(c.pages)
	c.station.ScrollTo(c.scroll, 0)
	c.scroll = 0
}

func (c *Cozy) ShowImage(r io.Reader) {
	i, _, err := image.Decode(r)
	if err != nil {
		c.Error(err, true)
		return
	}
	c.station.Clear()
	img := tview.NewImage().SetImage(i)
	c.pages.AddAndSwitchToPage("image", img, true)
	c.app.SetFocus(img)
}

func (c *Cozy) RequestUpload(u *url.URL) error {
	c.station.Clear()
	form := tview.NewForm()
	form.AddInputField("upload 💪", "", 0, nil, nil)

	form.AddButton("submit", func() {
		raw := form.GetFormItem(0).(*tview.InputField).GetText()
		c.pages.RemovePage("prompt")
		if c.cancel != nil {
			c.cancel()
		}
		length := int64(len(raw))
		data := strings.NewReader(raw)
		c.requests <- &Request{u.String(), false, length, data}
	})
	form.SetFieldBackgroundColor(c.theme.Status)
	form.SetFieldTextColor(c.theme.Text)
	form.SetButtonBackgroundColor(c.theme.Status)
	form.SetButtonTextColor(c.theme.Text)
	form.SetBorderPadding(4, 4, 4, 4)
	c.pages.AddPage("prompt", form, true, true)
	c.app.SetFocus(form)
	return nil

}

func (c *Cozy) RequestInput(u *url.URL, prompt string, secure bool) error {
	form := tview.NewForm()
	if secure {
		form.AddPasswordField(prompt, "", 0, '*', nil)
	} else {
		form.AddInputField(prompt, "", 0, nil, nil)
	}
	form.AddButton("submit", func() {
		raw := form.GetFormItem(0).(*tview.InputField).GetText()
		u.RawQuery = url.PathEscape(raw)
		c.pages.RemovePage("prompt")
		c.Go(u.String(), false)
	})
	form.SetFieldBackgroundColor(c.theme.Status)
	form.SetFieldTextColor(c.theme.Text)
	form.SetButtonBackgroundColor(c.theme.Status)
	form.SetButtonTextColor(c.theme.Text)
	form.SetBorderPadding(4, 4, 4, 4)
	c.pages.AddPage("prompt", form, true, true)
	c.app.SetFocus(form)
	return nil
}

func (c *Cozy) CheckDaylight() {
	tokyo := 35.6764

	now := time.Now()
	if yofukashi.Daytime(now, tokyo) {
		c.SetTheme(c.themes["day"])
	} else {
		c.SetTheme(c.themes["night"])
	}

	ticker := time.NewTicker(5000 * time.Millisecond)
	go func() {
		for {
			now := time.Now()
			select {
			case <-ticker.C:
				c.app.QueueUpdateDraw(func() {
					if yofukashi.Daytime(now, tokyo) {
						c.SetTheme(c.themes["day"])
					} else {
						c.SetTheme(c.themes["night"])
					}
				})
			}
		}
	}()
}

func edit(path string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Run()
}

func main() {
	b := flag.Bool("b", false, "edit bookmarks")
	t := flag.String("t", "", "set a theme (night/day)")
	flag.Parse()

	c := NewCozy(*t)
	c.InitializeBookmarks()
	c.InitializeTrust()

	if *b {
		edit(c.ConfigPath("bookmarks.gmi"))
		os.Exit(0)
	}

	if flag.NArg() > 0 {
		c.Go(flag.Arg(0), true)
		c.app.SetFocus(c.pages)
	} else {
		c.ShowFile("/gmi/cozy.gmi")
	}

	if c.app.Run() != nil {
		os.Exit(1)
	}

	fmt.Println("the world spins...")
}
