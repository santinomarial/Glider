//go:build linux

// Package snapshot owns per-container OverlayFS writable snapshots.
package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var (
	ErrInvalidSnapshot = errors.New("invalid snapshot")
	ErrMounted         = errors.New("snapshot is mounted")
)

var idRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type Record struct {
	Version   int      `json:"version"`
	ID        string   `json:"id"`
	LowerDirs []string `json:"lower_dirs"`
}

type Snapshot struct { ID, Upper, Work, Merged string }
type Manager struct{ root string }

func NewManager(root string) (*Manager, error) {
	if root == "" || !filepath.IsAbs(root) { return nil, fmt.Errorf("%w: snapshot root must be absolute", ErrInvalidSnapshot) }
	if err := os.MkdirAll(root, 0o755); err != nil { return nil, fmt.Errorf("create snapshot root: %w", err) }
	return &Manager{root: root}, nil
}

// Ensure creates and mounts id's snapshot. lowerDirs are ordered base-to-top;
// OverlayFS requires the reverse order in its lowerdir mount option.
func (m *Manager) Ensure(id string, lowerDirs []string) (Snapshot, error) {
	s, err := m.paths(id)
	if err != nil { return Snapshot{}, err }
	if len(lowerDirs) == 0 { return Snapshot{}, fmt.Errorf("%w: no lower layers", ErrInvalidSnapshot) }
	clean := make([]string, len(lowerDirs))
	for i, dir := range lowerDirs {
		if !filepath.IsAbs(dir) || strings.ContainsAny(dir, ",:") { return Snapshot{}, fmt.Errorf("%w: unsafe lowerdir %q", ErrInvalidSnapshot, dir) }
		info, err := os.Stat(dir); if err != nil || !info.IsDir() { return Snapshot{}, fmt.Errorf("%w: lowerdir %q is not a directory", ErrInvalidSnapshot, dir) }
		clean[i] = filepath.Clean(dir)
	}
	if mounted, err := isMountpoint(s.Merged); err != nil { return Snapshot{}, err } else if mounted { return s, nil }
	for _, dir := range []string{s.Upper, s.Work, s.Merged} { if err := os.MkdirAll(dir, 0o755); err != nil { return Snapshot{}, fmt.Errorf("create snapshot directory: %w", err) } }
	if err := sameFilesystem(s.Upper, s.Work); err != nil { return Snapshot{}, err }
	if err := saveRecord(filepath.Dir(s.Upper), Record{Version: 1, ID: id, LowerDirs: clean}); err != nil { return Snapshot{}, err }
	reversed := make([]string, len(clean)); for i := range clean { reversed[len(clean)-1-i] = clean[i] }
	data := "lowerdir="+strings.Join(reversed, ":")+",upperdir="+s.Upper+",workdir="+s.Work
	if err := syscall.Mount("overlay", s.Merged, "overlay", 0, data); err != nil { return Snapshot{}, fmt.Errorf("mount overlay snapshot %s: %w", id, err) }
	return s, nil
}

// Remove idempotently unmounts and deletes all Glider-owned snapshot state.
func (m *Manager) Remove(id string) error {
	s, err := m.paths(id); if err != nil { return err }
	mounted, err := isMountpoint(s.Merged); if err != nil { return err }
	if mounted { if err := syscall.Unmount(s.Merged, syscall.MNT_DETACH); err != nil { return fmt.Errorf("unmount snapshot %s: %w", id, err) } }
	if err := os.RemoveAll(filepath.Dir(s.Upper)); err != nil { return fmt.Errorf("remove snapshot %s: %w", id, err) }
	return nil
}

// Recover removes a recorded partial snapshot or confirms a mounted one. It is
// safe after a crash at any Ensure step.
func (m *Manager) Recover(id string) (Snapshot, error) {
	s, err := m.paths(id); if err != nil { return Snapshot{}, err }
	rec, err := loadRecord(filepath.Dir(s.Upper))
	if os.IsNotExist(err) { return Snapshot{}, err }
	if err != nil { return Snapshot{}, err }
	if rec.ID != id || rec.Version != 1 { return Snapshot{}, fmt.Errorf("%w: corrupt snapshot record", ErrInvalidSnapshot) }
	mounted, err := isMountpoint(s.Merged); if err != nil { return Snapshot{}, err }
	if mounted { return s, nil }
	if err := m.Remove(id); err != nil { return Snapshot{}, err }
	return Snapshot{}, os.ErrNotExist
}

func (m *Manager) paths(id string) (Snapshot, error) {
	if !idRE.MatchString(id) { return Snapshot{}, fmt.Errorf("%w: invalid id %q", ErrInvalidSnapshot, id) }
	base := filepath.Join(m.root, id)
	return Snapshot{ID:id, Upper:filepath.Join(base,"upper"), Work:filepath.Join(base,"work"), Merged:filepath.Join(base,"merged")}, nil
}

func sameFilesystem(a,b string) error { var sa,sb syscall.Stat_t; if err:=syscall.Stat(a,&sa);err!=nil{return err};if err:=syscall.Stat(b,&sb);err!=nil{return err};if sa.Dev!=sb.Dev{return fmt.Errorf("%w: upper and work directories are on different filesystems",ErrInvalidSnapshot)};return nil }

func saveRecord(dir string, rec Record) error { data,err:=json.MarshalIndent(rec,"","  ");if err!=nil{return err};tmp:=filepath.Join(dir,"state.json.tmp");final:=filepath.Join(dir,"state.json");f,err:=os.OpenFile(tmp,os.O_CREATE|os.O_TRUNC|os.O_WRONLY,0o644);if err!=nil{return err};if _,err=f.Write(data);err!=nil{f.Close();return err};if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err};if err=os.Rename(tmp,final);err!=nil{return err};if d,err:=os.Open(dir);err==nil{_=d.Sync();_=d.Close()};return nil }
func loadRecord(dir string)(Record,error){var rec Record;data,err:=os.ReadFile(filepath.Join(dir,"state.json"));if err!=nil{return rec,err};if err=json.Unmarshal(data,&rec);err!=nil{return rec,fmt.Errorf("%w: decode snapshot state: %v",ErrInvalidSnapshot,err)};return rec,nil}

func isMountpoint(target string) (bool,error) { data,err:=os.ReadFile("/proc/self/mountinfo");if err!=nil{return false,fmt.Errorf("read mountinfo: %w",err)};clean:=filepath.Clean(target);for _,line:=range strings.Split(string(data),"\n"){fields:=strings.Fields(line);if len(fields)>4&&unescapeMount(fields[4])==clean{return true,nil}};return false,nil }
func unescapeMount(s string)string{r:=strings.NewReplacer(`\040`," ",`\011`,"\t",`\012`,"\n",`\134`,`\`);return r.Replace(s)}
