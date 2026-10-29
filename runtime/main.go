package main

// main.go — CLI: parse flags, build Permissions, run the script.
//
//   minirt script.js [--allow-read[=a,b]] [--allow-write[=..]] [--allow-net[=h1,h2]]
//                    [--allow-env[=A,B]] [--timeout=5s] [--max-stack=N]
//                    [--virtual-time] [--trace] [-- script args]

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"minirt/minirt"
)

const usage = `usage: minirt <script.js> [flags] [-- args...]

flags (everything is denied unless granted):
  --allow-read[=PATH,...]   file reads (and require) under PATH; no value = all
  --allow-write[=PATH,...]  file writes under PATH; no value = all
  --allow-net[=HOST,...]    fetch to HOST or HOST:PORT; no value = all
  --allow-env[=NAME,...]    Sandbox.env.get; no value = all
  --timeout=DURATION        wall-clock limit (default 30s, 0 = none)
  --max-stack=N             max JS call depth (default 10000)
  --virtual-time            deterministic virtual clock (timers jump, Date.now() is virtual)
  --trace                   print the event-loop trace as JSON lines to stderr
`

type cliConfig struct {
	script string
	opts   minirt.Options
}

func parseArgs(args []string) (*cliConfig, error) {
	cfg := &cliConfig{opts: minirt.Options{Timeout: 30 * time.Second, MaxCallStack: 10000}}
	var readAll, writeAll bool
	var readPaths, writePaths []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			cfg.opts.Args = append(cfg.opts.Args, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") {
			if cfg.script == "" {
				cfg.script = a
			} else {
				cfg.opts.Args = append(cfg.opts.Args, a)
			}
			continue
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		list := func() []string {
			var out []string
			for _, s := range strings.Split(val, ",") {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
			return out
		}
		switch name {
		case "allow-read":
			readAll = !hasVal
			readPaths = append(readPaths, list()...)
		case "allow-write":
			writeAll = !hasVal
			writePaths = append(writePaths, list()...)
		case "allow-net":
			cfg.opts.Perms.Net = minirt.Grant{All: !hasVal, Items: list()}
		case "allow-env":
			cfg.opts.Perms.Env = minirt.Grant{All: !hasVal, Items: list()}
		case "timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return nil, fmt.Errorf("--timeout: %w", err)
			}
			cfg.opts.Timeout = d
		case "max-stack":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("--max-stack: invalid value %q", val)
			}
			cfg.opts.MaxCallStack = n
		case "virtual-time":
			cfg.opts.VirtualClock = true
		case "trace":
			enc := json.NewEncoder(os.Stderr)
			cfg.opts.Trace = func(e minirt.Event) { _ = enc.Encode(e) }
		case "help":
			return nil, errors.New("")
		default:
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
	}
	if cfg.script == "" {
		return nil, errors.New("no script given")
	}
	var err error
	if cfg.opts.Perms.Read, err = minirt.NewPathGrant(readAll, readPaths); err != nil {
		return nil, fmt.Errorf("--allow-read: %w", err)
	}
	if cfg.opts.Perms.Write, err = minirt.NewPathGrant(writeAll, writePaths); err != nil {
		return nil, fmt.Errorf("--allow-write: %w", err)
	}
	return cfg, nil
}

func main() {
	cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(os.Stderr, "error:", msg)
		}
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	rt, err := minirt.NewRuntime(cfg.opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := rt.RunFile(cfg.script); err != nil {
		fmt.Fprintln(os.Stderr, minirt.FormatError(err))
		os.Exit(minirt.ExitCode(err))
	}
}
