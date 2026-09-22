// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/data"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/tcell/v2"
	v1 "k8s.io/api/core/v1"
)

// logDownloadTimeout bounds a whole download so a chatty container cannot wedge
// the operation forever.
const logDownloadTimeout = 5 * time.Minute

// downloadedLog tracks one written file.
type downloadedLog struct {
	container string
	previous  bool
	path      string
	bytes     int64
}

// fullLogOptions asks the API for everything it still retains. Leaving
// TailLines nil is what separates a real download from the view buffer, which
// is capped by the logger tail and buffer settings.
func fullLogOptions(co string, previous bool) *v1.PodLogOptions {
	return &v1.PodLogOptions{
		Container:  co,
		Follow:     false,
		Timestamps: true,
		Previous:   previous,
		TailLines:  nil,
	}
}

// downloadLogFile streams one container log straight to disk.
func downloadLogFile(ctx context.Context, p *dao.Pod, fqn, dir, co string, previous bool) (*downloadedLog, error) {
	req, err := p.Logs(fqn, fullLogOptions(co, previous))
	if err != nil {
		return nil, err
	}
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cErr := stream.Close(); cErr != nil {
			slog.Debug("Closing log stream failed", slogs.Error, cErr)
		}
	}()

	name := fmt.Sprintf("%s-%s", strings.ReplaceAll(fqn, "/", "-"), co)
	if previous {
		name += "-previous"
	}
	path := filepath.Join(dir, data.SanitizeFileName(
		fmt.Sprintf("%s-%d.log", name, time.Now().Unix())))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	n, cErr := io.Copy(file, stream)
	if err := file.Close(); err != nil && cErr == nil {
		cErr = err
	}
	if cErr != nil {
		// A partial file is worse than none: it looks like a complete log.
		_ = os.Remove(path)
		return nil, cErr
	}
	if n == 0 {
		_ = os.Remove(path)
		return nil, nil
	}

	return &downloadedLog{container: co, previous: previous, path: path, bytes: n}, nil
}

// downloadLogs writes the full current and previous logs of every container in
// a pod. A missing previous log is normal (no restart yet) and never fails the
// download.
func downloadLogs(ctx context.Context, a *App, fqn string) ([]downloadedLog, error) {
	pod, err := fetchPod(a.factory, fqn)
	if err != nil {
		return nil, err
	}
	cc := fetchContainers(&pod.ObjectMeta, &pod.Spec, true)
	if len(cc) == 0 {
		return nil, fmt.Errorf("no containers on %q", fqn)
	}

	dir := a.Config.K9s.AppLogDownloadDir()
	if err := ensureDir(dir); err != nil {
		return nil, err
	}

	var (
		p    dao.Pod
		out  []downloadedLog
		errs error
	)
	p.Init(a.factory, client.PodGVR)
	for _, co := range cc {
		for _, previous := range []bool{false, true} {
			got, err := downloadLogFile(ctx, &p, fqn, dir, co, previous)
			if err != nil {
				// No previous log simply means the container never restarted.
				if !previous {
					errs = errors.Join(errs, fmt.Errorf("%s: %w", co, err))
				} else {
					slog.Debug("No previous log",
						slogs.Container, co,
						slogs.Error, err,
					)
				}
				continue
			}
			if got != nil {
				out = append(out, *got)
			}
		}
	}
	if len(out) == 0 && errs != nil {
		return nil, errs
	}

	return out, nil
}

// downloadLogsCmd wires the download onto a key. It runs off the UI goroutine
// because a large log can take a while to drain.
func (l *LogsExtender) downloadLogsCmd(evt *tcell.EventKey) *tcell.EventKey {
	path := l.GetTable().GetSelectedItem()
	if path == "" {
		return evt
	}
	if !isResourcePath(path) {
		path = l.GetTable().Path
	}
	if !isResourcePath(path) {
		l.App().Flash().Err(fmt.Errorf("nothing selected"))
		return nil
	}

	a := l.App()
	a.Flash().Infof("Downloading full logs for %s...", path)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), logDownloadTimeout)
		defer cancel()

		ll, err := downloadLogs(ctx, a, path)
		if err != nil {
			slog.Error("Log download failed", slogs.FQN, path, slogs.Error, err)
			a.Flash().Errf("Log download failed: %s", err)
			return
		}
		slog.Info("Logs downloaded",
			slogs.FQN, path,
			slogs.Dir, a.Config.K9s.AppLogDownloadDir(),
			slogs.Count, len(ll),
		)
		for _, d := range ll {
			slog.Info("Log file written", slogs.Path, d.path, slogs.Count, d.bytes)
		}
		a.Flash().Infof("Saved %d log file(s) to %s", len(ll), a.Config.K9s.AppLogDownloadDir())
	}()

	return nil
}
