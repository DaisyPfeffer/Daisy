// Command godm is a fast, resumable, multi-connection download manager.
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"godm/internal/config"
	"godm/internal/engine"
	"godm/internal/manager"
	"godm/internal/nativehost"
	"godm/internal/server"
)

const version = "1.0.0"

const usage = `GoDM %s — multi-connection download manager

Usage:
  godm-cli get <url> [flags]        download right now in this terminal (no server needed)
  godm-cli serve                    run the download server + web dashboard
  godm-cli ui                       print the dashboard address (with access token)
  godm-cli add <url>... [flags]     queue downloads on the running server
  godm-cli list                     show downloads
  godm-cli pause <id|all>           pause
  godm-cli resume <id|all>          resume
  godm-cli rm <id> [-delete]        remove (with -delete also deletes the finished file)
  godm-cli limit <KB/s>             set the global speed limit (0 = unlimited)
  godm-cli install-host [-chrome-id <id>] [-firefox]
                                register the browser helper for the extension
  godm-cli host                     browser helper mode (started by the browser)
  godm version

Run "godm-cli <command> -h" for the flags of a command.
`

func main() {
	log.SetFlags(0)
	args := os.Args[1:]

	// Browsers start the native host with the extension origin (Chrome/Edge)
	// or the manifest path + extension id (Firefox) as arguments.
	if len(args) > 0 && (strings.HasPrefix(args[0], "chrome-extension://") ||
		strings.HasSuffix(args[0], nativehost.Name+".json")) {
		args = []string{"host"}
	}
	if len(args) == 0 {
		// Double-clicked (Start menu, desktop icon): run the manager and open
		// the dashboard. Show help only when started from an interactive shell
		// on non-Windows systems.
		if runtime.GOOS == "windows" || !isTerminal() {
			args = []string{"serve", "-open"}
		} else {
			fmt.Fprintf(os.Stderr, usage, version)
			os.Exit(2)
		}
	}
	var err error
	switch args[0] {
	case "get":
		err = cmdGet(args[1:])
	case "serve":
		err = cmdServe(args[1:])
	case "ui":
		err = cmdUI()
	case "add":
		err = cmdAdd(args[1:])
	case "list", "ls":
		err = cmdList()
	case "pause":
		err = cmdSimple(args[1:], "pause")
	case "resume":
		err = cmdSimple(args[1:], "resume")
	case "rm", "remove":
		err = cmdRemove(args[1:])
	case "limit":
		err = cmdLimit(args[1:])
	case "host":
		err = cmdHost()
	case "install-host":
		err = cmdInstallHost(args[1:])
	case "version", "-v", "--version":
		fmt.Println("godm", version)
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n"+usage, args[0], version)
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("godm: %v", err)
	}
}

func loadConfig() (*config.Config, string) {
	home := config.Home()
	cfg, err := config.Load(home)
	if err != nil {
		log.Fatalf("godm: load config: %v", err)
	}
	return cfg, home
}

// ---------- request flags shared by get/add ----------

type headerList map[string]string

func (h headerList) String() string { return "" }
func (h headerList) Set(v string) error {
	k, val, ok := strings.Cut(v, ":")
	if !ok {
		return errors.New(`header must look like "Name: value"`)
	}
	h[strings.TrimSpace(k)] = strings.TrimSpace(val)
	return nil
}

type reqFlags struct {
	out, dir, referer, cookie, ua, user, pass, checksum string
	conns                                               int
	limitKB                                             int64
	headers                                             headerList
}

func addReqFlags(fs *flag.FlagSet) *reqFlags {
	r := &reqFlags{headers: headerList{}}
	fs.StringVar(&r.out, "o", "", "output file name")
	fs.StringVar(&r.dir, "d", "", "output folder")
	fs.IntVar(&r.conns, "c", 0, "connections (1-32, default from settings)")
	fs.StringVar(&r.referer, "referer", "", "Referer header")
	fs.StringVar(&r.cookie, "cookie", "", "Cookie header")
	fs.StringVar(&r.ua, "ua", "", "User-Agent")
	fs.StringVar(&r.user, "user", "", "username for HTTP authentication")
	fs.StringVar(&r.pass, "pass", "", "password for HTTP authentication")
	fs.StringVar(&r.checksum, "checksum", "", "verify, e.g. sha256:<hex>")
	fs.Int64Var(&r.limitKB, "limit", 0, "speed limit for this download in KB/s")
	fs.Var(r.headers, "H", `extra header "Name: value" (repeatable)`)
	return r
}

func (r *reqFlags) request(url string) engine.Request {
	req := engine.Request{
		URL: url, FileName: r.out, Dir: r.dir, Connections: r.conns, Referer: r.referer,
		Cookie: r.cookie, UserAgent: r.ua, Username: r.user, Password: r.pass,
		Checksum: r.checksum, SpeedLimit: r.limitKB * 1024,
	}
	if len(r.headers) > 0 {
		req.Headers = r.headers
	}
	return req
}

// ---------- get: standalone download ----------

func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	rf := addReqFlags(fs)
	fs.Parse(reorder(fs, args))
	if fs.NArg() != 1 {
		return errors.New("usage: godm-cli get <url> [flags]")
	}
	cfg, _ := loadConfig()
	eng := engine.New()
	eng.Connections = cfg.Connections
	eng.DefaultDir = "."
	if rf.dir == "" {
		rf.dir = "."
	}
	eng.Limiter = engine.NewLimiter(cfg.SpeedLimit)
	req := rf.request(fs.Arg(0))
	if req.Username == "" {
		if l, ok := cfg.LoginFor(req.URL); ok {
			req.Username, req.Password = l.Username, l.Password
		}
	}
	t := engine.NewTask("cli", req)
	rfile := resumeFile(req)
	if prev := loadResume(rfile); prev != nil {
		if _, err := os.Stat(prev.Path + engine.PartSuffix); err == nil {
			prev.Request = req // flags from this run win
			if prev.FileName == "" || req.FileName == "" {
				prev.FileName = filepath.Base(prev.Path)
			}
			t = engine.TaskFromInfo(*prev)
			fmt.Fprintf(os.Stderr, "resuming %s\n", prev.Path)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, t) }()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	start, last, lastT := time.Now(), int64(0), time.Now()
	var speed float64
	for {
		select {
		case err := <-done:
			i := t.Snapshot()
			fmt.Fprint(os.Stderr, "\r\033[K")
			if err != nil {
				saveResume(rfile, i)
				if ctx.Err() != nil {
					fmt.Fprintf(os.Stderr, "stopped at %s; run the same command again to resume\n", human(i.Downloaded))
					return nil
				}
				return err
			}
			os.Remove(rfile)
			fmt.Fprintf(os.Stderr, "saved %s (%s in %s)\n", i.Path, human(i.Size), time.Since(start).Round(100*time.Millisecond))
			return nil
		case now := <-tick.C:
			i := t.Snapshot()
			if last == 0 {
				last = i.Downloaded // do not count resumed bytes as speed
			}
			inst := float64(i.Downloaded-last) / now.Sub(lastT).Seconds()
			speed = 0.6*speed + 0.4*inst
			last, lastT = i.Downloaded, now
			fmt.Fprintf(os.Stderr, "\r\033[K%s", progressLine(i, speed))
		}
	}
}

// resume support for "get": a small state file in the output folder, keyed
// by URL + output name, so re-running the same command continues.
func resumeFile(req engine.Request) string {
	sum := sha1.Sum([]byte(req.URL + "\x00" + req.FileName))
	return filepath.Join(req.Dir, ".godm-"+hex.EncodeToString(sum[:6])+".json")
}

func saveResume(path string, i engine.Info) {
	if i.Path == "" || !i.Resumable {
		return
	}
	i.Cookie, i.Password = "", ""
	b, _ := json.Marshal(i)
	os.WriteFile(path, b, 0o600)
}

func loadResume(path string) *engine.Info {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r engine.Info
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func progressLine(i engine.Info, speed float64) string {
	name := i.FileName
	if len(name) > 32 {
		name = name[:29] + "..."
	}
	if i.Size <= 0 {
		return fmt.Sprintf("%-32s %10s  %9s/s", name, human(i.Downloaded), human(int64(speed)))
	}
	pct := float64(i.Downloaded) / float64(i.Size)
	const w = 28
	fill := int(pct * w)
	if fill > w {
		fill = w
	}
	eta := "—"
	if speed > 1 {
		eta = (time.Duration(float64(i.Size-i.Downloaded)/speed) * time.Second).Round(time.Second).String()
	}
	return fmt.Sprintf("%-32s [%s%s] %5.1f%% %9s/s  ETA %s", name, strings.Repeat("█", fill), strings.Repeat("░", w-fill), pct*100, human(int64(speed)), eta)
}

func human(n int64) string {
	if n < 0 {
		return "?"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// reorder lets flags appear after positional arguments ("godm-cli get URL -c 4").
func reorder(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue
			}
			if f := fs.Lookup(name); f != nil {
				if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
					continue
				}
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

// ---------- serve ----------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 0, "port (default from config)")
	open := fs.Bool("open", false, "open the dashboard in the web browser")
	fs.Parse(args)
	cfg, home := loadConfig()
	if *port != 0 {
		cfg.Port = *port
	}
	logger := log.New(os.Stderr, "godm ", log.LstdFlags)
	m, err := manager.New(cfg, home, logger)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	dashboard := fmt.Sprintf("http://%s/?token=%s", addr, cfg.Token)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		m.Close()
		if *open && alreadyRunning(addr, cfg.Token) {
			// GoDM is already running: just bring up its dashboard.
			return openBrowser(dashboard)
		}
		return fmt.Errorf("listen %s: %w (is godm already running?)", addr, err)
	}
	srv := &http.Server{Handler: server.New(m, cfg.Token).Handler(), ReadHeaderTimeout: 10 * time.Second}
	logger.Printf("dashboard: %s", dashboard)
	if *open {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := openBrowser(dashboard); err != nil {
				logger.Printf("could not open the browser: %v", err)
			}
		}()
	}
	logger.Printf("keep this window open while downloading; press Ctrl+C to quit")
	logger.Printf("saving to %s (config: %s)", cfg.DownloadDir, cfg.Path())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		m.Close()
		return err
	}
	logger.Printf("stopping, saving state…")
	return m.Close()
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func alreadyRunning(addr, token string) bool {
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/settings", nil)
	req.Header.Set("X-GoDM-Token", token)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func cmdUI() error {
	cfg, _ := loadConfig()
	fmt.Printf("http://127.0.0.1:%d/?token=%s\n", cfg.Port, cfg.Token)
	return nil
}

// ---------- API client ----------

type client struct {
	base, token string
	http        *http.Client
}

func newClient() *client {
	cfg, _ := loadConfig()
	return &client{base: fmt.Sprintf("http://127.0.0.1:%d", cfg.Port), token: cfg.Token, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-GoDM-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the GoDM server at %s — start it with \"godm-cli serve\"", c.base)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	rf := addReqFlags(fs)
	paused := fs.Bool("paused", false, "add without starting")
	at := fs.String("at", "", `start time, e.g. "2026-11-10 02:00" or "23:30" (local time)`)
	fs.Parse(reorder(fs, args))
	if fs.NArg() == 0 {
		return errors.New("usage: godm-cli add <url>... [flags]")
	}
	body := server.AddRequest{Request: rf.request(""), URLs: fs.Args(), Paused: *paused}
	if *at != "" {
		t, err := parseWhen(*at, time.Now())
		if err != nil {
			return err
		}
		body.StartAt = t
	}
	var added []engine.Info
	if err := newClient().do("POST", "/api/downloads", body, &added); err != nil {
		return err
	}
	for _, a := range added {
		fmt.Printf("%s  %s\n", a.ID, a.URL)
	}
	return nil
}

func parseWhen(s string, now time.Time) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("15:04", s, time.Local); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
		if at.Before(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("cannot understand time %q", s)
}

func cmdList() error {
	var list []engine.Info
	if err := newClient().do("GET", "/api/downloads", nil, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no downloads")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tPROGRESS\tSPEED\tNAME")
	for _, d := range list {
		prog := human(d.Downloaded)
		if d.Size > 0 {
			prog = fmt.Sprintf("%5.1f%% of %s", float64(d.Downloaded)/float64(d.Size)*100, human(d.Size))
		}
		speed := ""
		if d.Status == engine.StatusActive {
			speed = human(d.Speed) + "/s"
		}
		name := d.FileName
		if name == "" {
			name = d.URL
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Status, prog, speed, name)
		if d.Error != "" {
			fmt.Fprintf(tw, "\t\t\t\t  ↳ %s\n", d.Error)
		}
	}
	return tw.Flush()
}

func cmdSimple(args []string, action string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: godm %s <id|all>", action)
	}
	c := newClient()
	if args[0] == "all" {
		return c.do("POST", "/api/"+action+"-all", nil, nil)
	}
	return c.do("POST", "/api/downloads/"+args[0]+"/"+action, nil, nil)
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	del := fs.Bool("delete", false, "also delete the downloaded file")
	fs.Parse(reorder(fs, args))
	if fs.NArg() != 1 {
		return errors.New("usage: godm-cli rm <id> [-delete]")
	}
	q := ""
	if *del {
		q = "?deleteFile=1"
	}
	return newClient().do("DELETE", "/api/downloads/"+fs.Arg(0)+q, nil, nil)
}

func cmdLimit(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: godm-cli limit <KB/s>  (0 = unlimited)")
	}
	kb, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || kb < 0 {
		return errors.New("limit must be a number of KB/s (0 = unlimited)")
	}
	bps := kb * 1024
	return newClient().do("PUT", "/api/settings", manager.Settings{SpeedLimit: &bps}, nil)
}

// ---------- browser native host ----------

func cmdHost() error {
	c := newClient()
	return nativehost.Serve(os.Stdin, os.Stdout, func(m nativehost.Message) (string, error) {
		var added []engine.Info
		req := server.AddRequest{Request: engine.Request{
			URL: m.URL, Referer: m.Referer, Cookie: m.Cookie, UserAgent: m.UserAgent, FileName: m.FileName,
		}, Source: "browser"}
		if err := c.do("POST", "/api/downloads", req, &added); err != nil {
			return "", err
		}
		if len(added) == 0 {
			return "", errors.New("nothing added")
		}
		return added[0].ID, nil
	})
}

func cmdInstallHost(args []string) error {
	fs := flag.NewFlagSet("install-host", flag.ExitOnError)
	chromeID := fs.String("chrome-id", nativehost.ChromeExtensionID, "extension ID shown on chrome://extensions (Chrome, Edge, Brave)")
	firefox := fs.Bool("firefox", false, "also register for Firefox (extension id "+nativehost.FirefoxExtensionID+")")
	fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	var ids []string
	if *chromeID != "" {
		ids = []string{*chromeID}
	}
	done, err := nativehost.Install(exe, ids, *firefox)
	for _, d := range done {
		fmt.Println("registered", d)
	}
	if err != nil {
		return err
	}
	fmt.Println("done — restart the browser")
	return nil
}
