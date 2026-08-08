// Package agent implements gliderd's level-triggered desired-vs-actual loop.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/santinomarial/glider/internal/api"
)

type ObservedPhase string

const (
	ObservedAbsent  ObservedPhase = "ABSENT"
	ObservedRunning ObservedPhase = "RUNNING"
	ObservedFailed  ObservedPhase = "FAILED"
)

type Observed struct {
	Phase       ObservedPhase `json:"phase"`
	ContainerID string        `json:"container_id,omitempty"`
	Error       string        `json:"error,omitempty"`
}
type Driver interface {
	Ensure(context.Context, api.Assignment) (Observed, error)
	Remove(context.Context, api.Assignment, Observed) error
	Observe(context.Context, api.Assignment, Observed) (Observed, error)
}
type record struct {
	Version    int            `json:"version"`
	Assignment api.Assignment `json:"assignment"`
	Observed   Observed       `json:"observed"`
	UpdatedAt  time.Time      `json:"updated_at"`
}
type Reconciler struct {
	root   string
	driver Driver
	mu     sync.Mutex
}

func containerID(a api.Assignment) string {
	sum := sha256.Sum256([]byte(a.TaskID))
	return fmt.Sprintf("%x-g%d", sum[:10], a.Generation)
}

func New(root string, driver Driver) (*Reconciler, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("agent state root must be absolute")
	}
	if driver == nil {
		return nil, errors.New("agent driver is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Reconciler{root: root, driver: driver}, nil
}

// Reconcile is a full level-triggered pass. Event watches only wake this
// operation; durable desired state and observed kernel state determine action.
func (r *Reconciler) Reconcile(ctx context.Context, desired []api.Assignment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	records, err := r.load()
	if err != nil {
		return err
	}
	want := map[string]api.Assignment{}
	for _, a := range desired {
		if old, ok := want[a.TaskID]; ok && old.Generation >= a.Generation {
			continue
		}
		want[a.TaskID] = a
	}
	var errs []error
	for taskID, rec := range records {
		a, ok := want[taskID]
		if !ok || a.Generation > rec.Assignment.Generation {
			observed, observeErr := r.driver.Observe(ctx, rec.Assignment, rec.Observed)
			if observeErr != nil {
				errs = append(errs, fmt.Errorf("observe %s: %w", taskID, observeErr))
				continue
			}
			if err := r.driver.Remove(ctx, rec.Assignment, observed); err != nil {
				errs = append(errs, fmt.Errorf("remove %s generation %d: %w", taskID, rec.Assignment.Generation, err))
				continue
			}
			if err := r.delete(taskID); err != nil {
				errs = append(errs, err)
				continue
			}
			delete(records, taskID)
		}
		if ok && a.Generation < rec.Assignment.Generation {
			delete(want, taskID)
		}
	}
	keys := make([]string, 0, len(want))
	for id := range want {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, taskID := range keys {
		a := want[taskID]
		if rec, ok := records[taskID]; ok && rec.Assignment.Generation == a.Generation {
			observed, err := r.driver.Observe(ctx, a, rec.Observed)
			if err == nil && observed.Phase == ObservedRunning {
				rec.Observed = observed
				_ = r.save(taskID, rec)
				continue
			}
		}
		observed, err := r.driver.Ensure(ctx, a)
		if err != nil {
			observed.Phase = ObservedFailed
			observed.Error = err.Error()
			errs = append(errs, fmt.Errorf("ensure %s: %w", taskID, err))
		}
		rec := record{Version: 1, Assignment: a, Observed: observed, UpdatedAt: time.Now().UTC()}
		if saveErr := r.save(taskID, rec); saveErr != nil {
			errs = append(errs, saveErr)
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) load() (map[string]record, error) {
	out := map[string]record{}
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var rec record
		if err := json.Unmarshal(data, &rec); err != nil || rec.Version != 1 || rec.Assignment.TaskID == "" {
			return nil, fmt.Errorf("corrupt agent record %s", entry.Name())
		}
		out[rec.Assignment.TaskID] = rec
	}
	return out, nil
}
func (r *Reconciler) save(taskID string, rec record) error {
	if !safeID(taskID) {
		return errors.New("unsafe task ID")
	}
	rec.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.root, taskID+".json.tmp")
	final := filepath.Join(r.root, taskID+".json")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, final); err != nil {
		return err
	}
	if d, err := os.Open(r.root); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
func (r *Reconciler) delete(taskID string) error {
	if !safeID(taskID) {
		return errors.New("unsafe task ID")
	}
	err := os.Remove(filepath.Join(r.root, taskID+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func safeID(s string) bool {
	return s != "" && !filepath.IsAbs(s) && filepath.Base(s) == s && s != "." && s != ".."
}
