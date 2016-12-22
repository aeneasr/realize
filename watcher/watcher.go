package cli

import (
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"log"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type pollWatcher struct {
	paths map[string]bool
}

func (w *pollWatcher) isWatching(path string) bool {
	a, b := w.paths[path]
	return a && b
}

func (w *pollWatcher) Add(path string) error {
	if w.paths == nil {
		w.paths = map[string]bool{}
	}
	w.paths[path] = true
	return nil
}

func (p *Project) watchByPolling() {
	var wr sync.WaitGroup
	var watcher = new(pollWatcher)
	channel, exit := make(chan bool, 1), make(chan bool, 1)
	p.path = p.Path
	defer func() {
		wg.Done()
	}()
	p.cmd(exit)
	if err := p.walks(watcher); err != nil {
		log.Fatalln(p.pname(p.Name, 2), ":", p.Red.Bold(err.Error()))
		return
	}
	go p.routines(channel, &wr)
	p.LastChangedOn = time.Now().Truncate(time.Second)
	// waiting for an event

	var walk = func(changed string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		} else if !watcher.isWatching(changed) {
			return nil
		} else if !info.ModTime().Truncate(time.Second).After(p.LastChangedOn) {
			return nil
		}

		var ext string
		if index := strings.Index(filepath.Ext(changed), "_"); index == -1 {
			ext = filepath.Ext(changed)
		} else {
			ext = filepath.Ext(changed)[0:index]
		}
		i := strings.Index(changed, filepath.Ext(changed))
		file := changed[:i] + ext
		path := filepath.Dir(changed[:i])
		if changed[:i] != "" && inArray(ext, p.Watcher.Exts) {
			p.LastChangedOn = time.Now().Truncate(time.Second)
			msg := fmt.Sprintln(p.pname(p.Name, 4), ":", p.Magenta.Bold(strings.ToUpper(ext[1:]) + " changed"), p.Magenta.Bold(file))
			out := BufferOut{Time: time.Now(), Text: strings.ToUpper(ext[1:]) + " changed " + file}
			p.print("log", out, msg, "")
			// stop and run again
			if p.Run {
				close(channel)
				channel = make(chan bool)
			}
			// handle multiple errors, need a better way
			p.fmt(file)
			p.test(path)
			p.generate(path)
			go p.routines(channel, &wr)
		}

		return nil
	}
	for {
		for _, dir := range p.Watcher.Paths {
			base := filepath.Join(p.base, dir)
			if _, err := os.Stat(base); err == nil {
				if err := filepath.Walk(base, walk); err != nil {
					log.Println(p.Red.Bold(err.Error()))
				}
			} else {
				log.Println(p.Red.Bold(base + " path doesn't exist"))
			}

			select {
			case <-exit:
				return
			case <-time.After(p.parent.Config.PollingInterval / time.Duration(len(p.Watcher.Paths))):
			}
		}
	}
}

// Watching method is the main core. It manages the livereload and the watching
func (p *Project) watching() {
	var wr sync.WaitGroup
	var watcher *fsnotify.Watcher
	channel, exit := make(chan bool, 1), make(chan bool, 1)
	p.path = p.Path
	watcher, err := fsnotify.NewWatcher()
	defer func() {
		wg.Done()
	}()
	if err != nil {
		log.Fatalln(p.pname(p.Name, 2), ":", p.Red.Bold(err.Error()))
		return
	}
	p.cmd(exit)
	if p.walks(watcher) != nil {
		log.Fatalln(p.pname(p.Name, 2), ":", p.Red.Bold(err.Error()))
		return
	}
	go p.routines(channel, &wr)
	p.LastChangedOn = time.Now().Truncate(time.Second)
	// waiting for an event
	for {
		select {
		case event := <-watcher.Events:
			if time.Now().Truncate(time.Second).After(p.LastChangedOn) {
				if event.Op & fsnotify.Chmod == fsnotify.Chmod {
					continue
				}
				if _, err := os.Stat(event.Name); err == nil {
					var ext string
					if index := strings.Index(filepath.Ext(event.Name), "_"); index == -1 {
						ext = filepath.Ext(event.Name)
					} else {
						ext = filepath.Ext(event.Name)[0:index]
					}
					i := strings.Index(event.Name, filepath.Ext(event.Name))
					file := event.Name[:i] + ext
					path := filepath.Dir(event.Name[:i])
					if event.Name[:i] != "" && inArray(ext, p.Watcher.Exts) {
						msg := fmt.Sprintln(p.pname(p.Name, 4), ":", p.Magenta.Bold(strings.ToUpper(ext[1:]) + " changed"), p.Magenta.Bold(file))
						out := BufferOut{Time: time.Now(), Text: strings.ToUpper(ext[1:]) + " changed " + file}
						p.print("log", out, msg, "")
						// stop and run again
						if p.Run {
							close(channel)
							channel = make(chan bool)
						}
						// handle multiple errors, need a better way
						p.fmt(file)
						p.test(path)
						p.generate(path)
						go p.routines(channel, &wr)
						p.LastChangedOn = time.Now().Truncate(time.Second)
					}
				}
			}
		case err := <-watcher.Errors:
			log.Println(p.Red.Bold(err.Error()))
		case <-exit:
			return
		}
	}
}

// Install calls an implementation of "go install"
func (p *Project) install() error {
	if p.Bin {
		start := time.Now()
		log.Println(p.pname(p.Name, 1), ":", "Installing..")
		stream, err := p.goInstall()
		if err != nil {
			msg := fmt.Sprintln(p.pname(p.Name, 2), ":", p.Red.Bold("Go Install"), p.Red.Regular(err.Error()))
			out := BufferOut{Time: time.Now(), Text: err.Error(), Type: "Go Install", Stream: stream}
			p.print("error", out, msg, stream)
		} else {
			msg := fmt.Sprintln(p.pname(p.Name, 5), ":", p.Green.Regular("Installed") + " after", p.Magenta.Regular(big.NewFloat(float64(time.Since(start).Seconds())).Text('f', 3), " s"))
			out := BufferOut{Time: time.Now(), Text: "Installed after " + big.NewFloat(float64(time.Since(start).Seconds())).Text('f', 3) + " s"}
			p.print("log", out, msg, stream)
		}
		return err
	}
	return nil
}

// Install calls an implementation of "go run"
func (p *Project) run(channel chan bool, wr *sync.WaitGroup) {
	if p.Run {
		start := time.Now()
		runner := make(chan bool, 1)
		log.Println(p.pname(p.Name, 1), ":", "Running..")
		go p.goRun(channel, runner, wr)
		for {
			select {
			case <-runner:
				msg := fmt.Sprintln(p.pname(p.Name, 5), ":", p.Green.Regular("Has been run") + " after", p.Magenta.Regular(big.NewFloat(float64(time.Since(start).Seconds())).Text('f', 3), " s"))
				out := BufferOut{Time: time.Now(), Text: "Has been run after " + big.NewFloat(float64(time.Since(start).Seconds())).Text('f', 3) + " s"}
				p.print("log", out, msg, "")
				return
			}
		}
	}
}

// Build calls an implementation of the "go build"
func (p *Project) build() error {
	if p.Build {
		start := time.Now()
		log.Println(p.pname(p.Name, 1), ":", "Building..")
		stream, err := p.goBuild()
		if err != nil {
			msg := fmt.Sprintln(p.pname(p.Name, 2), ":", p.Red.Bold("Go Build"), p.Red.Regular(err.Error()))
			out := BufferOut{Time: time.Now(), Text: err.Error(), Type: "Go Build", Stream: stream}
			p.print("error", out, msg, stream)
		} else {
			msg := fmt.Sprintln(p.pname(p.Name, 5), ":", p.Green.Regular("Builded") + " after", p.Magenta.Regular(big.NewFloat(float64(time.Since(start).Seconds())).Text('f', 3), " s"))
			out := BufferOut{Time: time.Now(), Text: "Builded after " + big.NewFloat(float64(time.Since(start).Seconds())).Text('f', 3) + " s"}
			p.print("log", out, msg, stream)
		}
		return err
	}
	return nil
}

// Fmt calls an implementation of the "go fmt"
func (p *Project) fmt(path string) error {
	if p.Fmt && strings.HasSuffix(path, ".go") {
		if stream, err := p.goTools(p.base, "gofmt", "-s", "-w", "-e", path); err != nil {
			msg := fmt.Sprintln(p.pname(p.Name, 2), ":", p.Red.Bold("Go Fmt"), p.Red.Regular("there are some errors in"), ":", p.Magenta.Bold(path))
			out := BufferOut{Time: time.Now(), Text: "there are some errors in", Path: path, Type: "Go Fmt", Stream: stream}
			p.print("error", out, msg, stream)
			return err
		}
	}
	return nil
}

// Generate calls an implementation of the "go generate"
func (p *Project) generate(path string) error {
	if p.Generate {
		if stream, err := p.goTools(path, "go", "generate"); err != nil {
			msg := fmt.Sprintln(p.pname(p.Name, 2), ":", p.Red.Bold("Go Generate"), p.Red.Regular("there are some errors in"), ":", p.Magenta.Bold(path))
			out := BufferOut{Time: time.Now(), Text: "there are some errors in", Path: path, Type: "Go Generate", Stream: stream}
			p.print("error", out, msg, stream)
			return err
		}
	}
	return nil
}

// Test calls an implementation of the "go test"
func (p *Project) test(path string) error {
	if p.Test {
		if stream, err := p.goTools(path, "go", "test"); err != nil {
			msg := fmt.Sprintln(p.pname(p.Name, 2), ":", p.Red.Bold("Go Test"), p.Red.Regular("there are some errors in "), ":", p.Magenta.Bold(path))
			out := BufferOut{Time: time.Now(), Text: "there are some errors in", Path: path, Type: "Go Test", Stream: stream}
			p.print("error", out, msg, stream)
			return err
		}
	}
	return nil
}

// Cmd calls an wrapper for execute the commands after/before
func (p *Project) cmd(exit chan bool) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	cast := func(commands []string) {
		for _, command := range commands {
			errors, logs := p.afterBefore(command)
			msg := fmt.Sprintln(p.pname(p.Name, 5), ":", p.Green.Bold("Command"), p.Green.Bold("\"") + command + p.Green.Bold("\""))
			out := BufferOut{Time: time.Now(), Text: command, Type: "After/Before"}
			if logs != "" {
				p.print("log", out, msg, "")
			}
			if errors != "" {
				p.print("error", out, msg, "")
			}
			if logs != "" {
				msg = fmt.Sprintln(logs)
				out = BufferOut{Time: time.Now(), Text: logs, Type: "After/Before"}
				p.print("log", out, "", msg)
			}
			if errors != "" {
				msg = fmt.Sprintln(p.Red.Regular(errors))
				out = BufferOut{Time: time.Now(), Text: errors, Type: "After/Before"}
				p.print("error", out, "", msg)
			}
		}
	}

	if len(p.Watcher.Before) > 0 {
		cast(p.Watcher.Before)
	}

	go func() {
		for {
			select {
			case <-c:
				if len(p.Watcher.After) > 0 {
					cast(p.Watcher.After)
				}
				close(exit)
			}
		}
	}()
}

type watcher interface {
	Add(path string) error
}

// Walks the file tree of a project
func (p *Project) walks(watcher watcher) error {
	var files, folders int64
	wd, _ := os.Getwd()
	walk := func(path string, info os.FileInfo, err error) error {
		if !p.ignore(path) {
			if (info.IsDir() && len(filepath.Ext(path)) == 0 && !strings.HasPrefix(path, ".")) && !strings.Contains(path, "/.") || (inArray(filepath.Ext(path), p.Watcher.Exts)) {
				if p.Watcher.Preview {
					log.Println(p.pname(p.Name, 1), ":", path)
				}
				if err = watcher.Add(path); err != nil {
					return filepath.SkipDir
				}
				if inArray(filepath.Ext(path), p.Watcher.Exts) {
					files++
					p.fmt(path)
				} else {
					folders++
					p.generate(path)
					p.test(path)
				}
			}
		}
		return nil
	}

	if p.path == "." || p.path == "/" {
		p.base = wd
		p.path = p.Wdir()
	} else if filepath.IsAbs(p.path) {
		p.base = p.path
	} else {
		p.base = filepath.Join(wd, p.path)
	}
	for _, dir := range p.Watcher.Paths {
		base := filepath.Join(p.base, dir)
		if _, err := os.Stat(base); err == nil {
			if err := filepath.Walk(base, walk); err != nil {
				log.Println(p.Red.Bold(err.Error()))
			}
		} else {
			return errors.New(base + " path doesn't exist")
		}
	}
	msg := fmt.Sprintln(p.pname(p.Name, 1), ":", p.Blue.Bold("Watching"), p.Magenta.Bold(files), "file/s", p.Magenta.Bold(folders), "folder/s")
	out := BufferOut{Time: time.Now(), Text: "Watching " + strconv.FormatInt(files, 10) + " files/s " + strconv.FormatInt(folders, 10) + " folder/s"}
	p.print("log", out, msg, "")
	return nil
}

// Ignore and validate a path
func (p *Project) ignore(str string) bool {
	for _, v := range p.Watcher.Ignore {
		if strings.Contains(str, filepath.Join(p.base, v)) {
			return true
		}
	}
	return false
}

// Routines launches the toolchain run, build, install
func (p *Project) routines(channel chan bool, wr *sync.WaitGroup) {
	install := p.install()
	build := p.build()
	wr.Add(1)
	if install == nil && build == nil {
		go p.run(channel, wr)
	}
	wr.Wait()
}

// Defines the colors scheme for the project name
func (p *Project) pname(name string, color int) string {
	switch color {
	case 1:
		name = p.Yellow.Regular("[") + strings.ToUpper(name) + p.Yellow.Regular("]")
		break
	case 2:
		name = p.Yellow.Regular("[") + p.Red.Bold(strings.ToUpper(name)) + p.Yellow.Regular("]")
		break
	case 3:
		name = p.Yellow.Regular("[") + p.Blue.Bold(strings.ToUpper(name)) + p.Yellow.Regular("]")
		break
	case 4:
		name = p.Yellow.Regular("[") + p.Magenta.Bold(strings.ToUpper(name)) + p.Yellow.Regular("]")
		break
	case 5:
		name = p.Yellow.Regular("[") + p.Green.Bold(strings.ToUpper(name)) + p.Yellow.Regular("]")
		break
	}
	return name
}

// Print on files, cli, ws
func (p *Project) print(t string, o BufferOut, msg string, stream string) {
	switch t {
	case "out":
		p.Buffer.StdOut = append(p.Buffer.StdOut, o)
		if p.File.Streams {
			f := p.Create(p.base, p.parent.Resources.Streams)
			t := time.Now()
			s := []string{t.Format("2006-01-02 15:04:05"), strings.ToUpper(p.Name), ":", o.Text, "\r\n"}
			if _, err := f.WriteString(strings.Join(s, " ")); err != nil {
				p.Fatal(err, "")
			}
		}
		if msg != "" && p.Cli.Streams {
			log.Print(msg)
		}
	case "log":
		p.Buffer.StdLog = append(p.Buffer.StdLog, o)
		if p.File.Logs {
			f := p.Create(p.base, p.parent.Resources.Logs)
			t := time.Now()
			s := []string{t.Format("2006-01-02 15:04:05"), strings.ToUpper(p.Name), ":", o.Text, "\r\n"}
			if stream != "" {
				s = []string{t.Format("2006-01-02 15:04:05"), strings.ToUpper(p.Name), ":", o.Text, "\r\n", stream}
			}
			if _, err := f.WriteString(strings.Join(s, " ")); err != nil {
				p.Fatal(err, "")
			}
		}
		if msg != "" {
			log.Print(msg)
		}
	case "error":
		p.Buffer.StdErr = append(p.Buffer.StdErr, o)
		if p.File.Errors {
			f := p.Create(p.base, p.parent.Resources.Errors)
			t := time.Now()
			s := []string{t.Format("2006-01-02 15:04:05"), strings.ToUpper(p.Name), ":", o.Type, o.Text, o.Path, "\r\n"}
			if stream != "" {
				s = []string{t.Format("2006-01-02 15:04:05"), strings.ToUpper(p.Name), ":", o.Type, o.Text, o.Path, "\r\n", stream}
			}
			if _, err := f.WriteString(strings.Join(s, " ")); err != nil {
				p.Fatal(err, "")
			}
		}
		if msg != "" {
			log.Print(msg)
		}
	}
	if stream != "" {
		fmt.Print(stream)
	}
	go func() {
		p.parent.Sync <- "sync"
	}()
}
