package kanban

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	configpkg "curvature/backend/internal/config"
)

const (
	stageTemplateFile = "stage_template.json"
	taskTemplateFile  = "task_template.json"
)

type TemplateStore struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
}

var bundledTaskTemplatePaths = defaultBundledTaskTemplatePaths

func NewTemplateStore() (*TemplateStore, error) {
	dir, err := configpkg.CurvatureConfigDir()
	if err != nil {
		return nil, err
	}
	return &TemplateStore{dir: dir, now: time.Now}, nil
}

func NewTemplateStoreAt(dir string) *TemplateStore {
	return &TemplateStore{dir: dir, now: time.Now}
}

func (s *TemplateStore) ListStageTemplates() ([]StageTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadStageTemplatesLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (s *TemplateStore) SaveStageTemplate(in StageTemplate) (StageTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadStageTemplatesLocked()
	if err != nil {
		return StageTemplate{}, err
	}
	now := s.now().UTC()
	in = normalizeStageTemplate(in)
	if in.Name == "" {
		return StageTemplate{}, errors.New("stage template name required")
	}
	if in.Role != RoleUser && in.Role != RoleAgent {
		return StageTemplate{}, errors.New("invalid stage role")
	}
	if in.Role == RoleAgent {
		if strings.TrimSpace(in.Agent) == "" {
			return StageTemplate{}, errors.New("agent stage requires agent")
		}
	}
	if in.ID == "" {
		in.ID = newID("stage")
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	replaced := false
	for i := range items {
		if items[i].ID == in.ID {
			items[i] = in
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, in)
	}
	return in, s.saveJSONLocked(stageTemplateFile, items)
}

func (s *TemplateStore) DeleteStageTemplate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("stage template id required")
	}
	items, err := s.loadStageTemplatesLocked()
	if err != nil {
		return err
	}
	next := items[:0]
	removed := false
	for _, item := range items {
		if item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return errors.New("stage template not found")
	}
	return s.saveJSONLocked(stageTemplateFile, next)
}

func (s *TemplateStore) ListTaskTemplates() ([]TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadTaskTemplatesLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (s *TemplateStore) GetTaskTemplate(id string) (TaskTemplate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return TaskTemplate{}, errors.New("task template id required")
	}
	items, err := s.ListTaskTemplates()
	if err != nil {
		return TaskTemplate{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return TaskTemplate{}, errors.New("task template not found")
}

func (s *TemplateStore) SaveTaskTemplate(in TaskTemplate) (TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadTaskTemplatesLocked()
	if err != nil {
		return TaskTemplate{}, err
	}
	now := s.now().UTC()
	in = normalizeTaskTemplate(in)
	if in.Name == "" {
		return TaskTemplate{}, errors.New("task template name required")
	}
	if len(in.Stages) == 0 {
		return TaskTemplate{}, errors.New("task template requires stages")
	}
	sort.SliceStable(in.Stages, func(i, j int) bool { return in.Stages[i].Position < in.Stages[j].Position })
	if in.Stages[0].Snapshot.Role != RoleUser {
		return TaskTemplate{}, errors.New("first stage must be user")
	}
	if in.ID == "" {
		in.ID = newID("tmpl")
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	for i := range in.Stages {
		if in.Stages[i].ID == "" {
			in.Stages[i].ID = newID("tmpl_stage")
		}
		in.Stages[i].Position = i
		in.Stages[i].Snapshot = normalizeStageTemplate(in.Stages[i].Snapshot)
		if in.Stages[i].Snapshot.Role == RoleAgent {
			if strings.TrimSpace(in.Stages[i].Snapshot.Agent) == "" {
				return TaskTemplate{}, errors.New("agent stage requires agent")
			}
		}
	}
	if in.MaxConcurrency <= 0 {
		in.MaxConcurrency = 1
	}
	replaced := false
	for i := range items {
		if items[i].ID == in.ID {
			items[i] = in
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, in)
	}
	return in, s.saveJSONLocked(taskTemplateFile, items)
}

func (s *TemplateStore) DeleteTaskTemplate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("task template id required")
	}
	items, err := s.loadTaskTemplatesLocked()
	if err != nil {
		return err
	}
	next := items[:0]
	removed := false
	for _, item := range items {
		if item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return errors.New("task template not found")
	}
	return s.saveJSONLocked(taskTemplateFile, next)
}

func (s *TemplateStore) loadStageTemplatesLocked() ([]StageTemplate, error) {
	var items []StageTemplate
	if err := s.loadJSONLocked(stageTemplateFile, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []StageTemplate{}
	}
	return items, nil
}

func (s *TemplateStore) loadTaskTemplatesLocked() ([]TaskTemplate, error) {
	var items []TaskTemplate
	path := filepath.Join(s.dir, taskTemplateFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		items, err = loadBundledTaskTemplates()
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			if err := s.saveJSONLocked(taskTemplateFile, items); err != nil {
				return nil, err
			}
		}
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, err
		}
	}
	if items == nil {
		items = []TaskTemplate{}
	}
	return items, nil
}

func loadBundledTaskTemplates() ([]TaskTemplate, error) {
	for _, path := range bundledTaskTemplatePaths() {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return []TaskTemplate{}, nil
		}
		var items []TaskTemplate
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, err
		}
		if items == nil {
			items = []TaskTemplate{}
		}
		return items, nil
	}
	return []TaskTemplate{}, nil
}

func defaultBundledTaskTemplatePaths() []string {
	paths := []string{}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		paths = append(paths,
			filepath.Join(exeDir, taskTemplateFile),
			filepath.Join(filepath.Dir(exeDir), "share", "curvature", taskTemplateFile),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, taskTemplateFile))
	}
	return uniqueNonEmptyPaths(paths)
}

func uniqueNonEmptyPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func (s *TemplateStore) loadJSONLocked(name string, out any) error {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (s *TemplateStore) saveJSONLocked(name string, value any) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(s.dir, name), data, 0o644)
}

func normalizeStageTemplate(in StageTemplate) StageTemplate {
	in.ID = strings.TrimSpace(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	in.Role = strings.TrimSpace(in.Role)
	if in.Role == "" {
		in.Role = RoleUser
	}
	in.Agent = strings.TrimSpace(in.Agent)
	in.Model = strings.TrimSpace(in.Model)
	in.Mode = strings.TrimSpace(in.Mode)
	in.Effort = strings.TrimSpace(in.Effort)
	in.FastService = strings.TrimSpace(in.FastService)
	in.SessionReusePolicy = strings.TrimSpace(in.SessionReusePolicy)
	if in.SessionReusePolicy == "" {
		in.SessionReusePolicy = SessionReuseTaskMain
	}
	in.PromptTemplate = strings.TrimSpace(in.PromptTemplate)
	return in
}

func normalizeTaskTemplate(in TaskTemplate) TaskTemplate {
	in.ID = strings.TrimSpace(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	return in
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
