package main // import "zvelo.io/watcher"

import (
	"flag"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"zvelo.io/watcher/command"
)

const debounceTime = 100 * time.Millisecond

var (
	timeout        time.Duration
	logFile        string
	cmd            command.Command
	restartCh      = make(chan error)
	debounceTimers = map[string]*time.Timer{}
)

func init() {
	flag.DurationVar(&timeout, "timeout", command.DefaultTimeout, "amount of time to wait for command to complete after being signaled before it is killed")
	flag.StringVar(&logFile, "logfile", "", "if specified, stdout and stderr will be sent to this file")
}

func main() {
	flag.Parse()

	if err := setup(); err != nil {
		log.Fatal(err)
	}

	err := run()

	if code, ok := command.ExitCode(err); ok && code != 0 {
		log.Printf("watcher: command completed, exit code: %d, error: %s", code, err)
		os.Exit(code)
	}

	if err != nil {
		log.Println("watcher: command completed with error:", err)
		os.Exit(1)
	}

	log.Println("watcher: command completed successfully")
}

func eventContainsAny(event fsnotify.Event, ops ...fsnotify.Op) bool {
	for _, op := range ops {
		if event.Op&op == op {
			return true
		}
	}
	return false
}

func debounce(name string, fn func()) {
	if timer, ok := debounceTimers[name]; ok && timer != nil {
		timer.Reset(debounceTime)
		return
	}

	debounceTimers[name] = time.AfterFunc(debounceTime, func() {
		delete(debounceTimers, name)
		fn()
	})
}

func handleEvent(event fsnotify.Event) {
	if eventContainsAny(event, fsnotify.Remove, fsnotify.Rename) {
		debounce("stop", func() {
			log.Printf("watcher: %s disappeared, stopping", cmd)
			cmd.Signal(syscall.SIGTERM)
		})
		return
	}

	if !eventContainsAny(event, fsnotify.Write, fsnotify.Create) {
		return
	}

	debounce("restart", func() {
		log.Printf("watcher: restarting %s due to %s", cmd, event.Op)
		if err := cmd.Restart(); err != nil {
			restartCh <- err
		}
	})
}

func setup() error {
	if len(flag.Args()) == 0 {
		return fmt.Errorf("watcher: command is required")
	}

	var err error

	if cmd, err = command.New(flag.Args()[0], flag.Args()...); err != nil {
		return err
	}

	if err = cmd.LogToFile(logFile); err != nil {
		return err
	}

	cmd.SetTimeout(timeout)

	return nil
}

func run() error {
	if err := cmd.Start(); err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	defer func() {
		if err = watcher.Close(); err != nil {
			log.Printf("watcher: error closing watcher: %s", err)
		}
	}()

	log.Println("watcher: watching", cmd.Path())

	if err := watcher.Add(cmd.Path()); err != nil {
		return err
	}

	for {
		select {
		case <-cmd.Done():
			return cmd.Err()
		case err = <-watcher.Errors:
			return err
		case event := <-watcher.Events:
			handleEvent(event)
		case err = <-restartCh:
			return err
		}
	}
}
